// Package site owns the bounded singleton presentation and rules settings.
package site

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gotthboard/gotth-bb/internal/policy"
	contentrender "github.com/gotthboard/gotth-bb/internal/render"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/text/unicode/norm"
)

const mutationTimeout = 2 * time.Second

var (
	ErrInput       = errors.New("invalid site settings input")
	ErrDenied      = errors.New("site settings administration denied")
	ErrConflict    = errors.New("site settings conflict")
	ErrUnavailable = errors.New("site settings unavailable")
)

type ShellPresentation struct {
	Name        string
	Description string
	Theme       string
}

type PublicRules struct {
	Shell ShellPresentation
	HTML  contentrender.TrustedHTML
}

type EditableSettings struct {
	Shell           ShellPresentation
	RulesMarkdown   string
	RendererVersion string
	Revision        int64
}

type SettingsInput struct {
	Name          string
	Description   string
	Theme         string
	RulesMarkdown string
	Reason        string
	Revision      int64
}

type MutationResult struct {
	Revision int64
	AuditID  int64
}

type shellQuerier interface {
	LoadSiteShellPresentation(context.Context) (db.LoadSiteShellPresentationRow, error)
}

type rulesQuerier interface {
	LoadPublicRules(context.Context) (db.LoadPublicRulesRow, error)
}

type editableQuerier interface {
	LoadEditableSiteSettings(context.Context, db.LoadEditableSiteSettingsParams) (db.LoadEditableSiteSettingsRow, error)
}

type transactionBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// LoadShell reads only the small presentation tuple used by complete pages.
//
// Complexity: local work is O(n), bounded by 360 Unicode scalars, with O(1)
// auxiliary space; one primary-key database read is delegated.
func LoadShell(ctx context.Context, querier shellQuerier) (ShellPresentation, error) {
	if ctx == nil || querier == nil {
		return ShellPresentation{}, fmt.Errorf("site shell loader is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return ShellPresentation{}, fmt.Errorf("load site shell: %w", err)
	}
	row, err := querier.LoadSiteShellPresentation(ctx)
	if err != nil {
		return ShellPresentation{}, fmt.Errorf("%w: load shell", ErrUnavailable)
	}
	shell := ShellPresentation{Name: row.SiteName, Description: row.SiteDescription, Theme: row.BrandTheme}
	if !validShell(shell) {
		return ShellPresentation{}, fmt.Errorf("%w: malformed shell", ErrUnavailable)
	}
	return shell, nil
}

// LoadRules returns one validated shell/rules projection. Persisted HTML is
// accepted only when it exactly matches the admitted renderer output.
func LoadRules(ctx context.Context, querier rulesQuerier) (PublicRules, error) {
	if ctx == nil || querier == nil {
		return PublicRules{}, fmt.Errorf("public rules loader is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return PublicRules{}, fmt.Errorf("load public rules: %w", err)
	}
	row, err := querier.LoadPublicRules(ctx)
	if err != nil {
		return PublicRules{}, fmt.Errorf("%w: load rules", ErrUnavailable)
	}
	shell := ShellPresentation{Name: row.SiteName, Description: row.SiteDescription, Theme: row.BrandTheme}
	trusted, err := validateRulesTuple(row.RulesMarkdown, row.RulesHtml, row.RulesRendererVersion)
	if !validShell(shell) || err != nil {
		return PublicRules{}, fmt.Errorf("%w: malformed rules", ErrUnavailable)
	}
	return PublicRules{Shell: shell, HTML: trusted}, nil
}

// LoadEditable returns one authorization-first administrator projection.
func LoadEditable(ctx context.Context, querier editableQuerier, actor policy.AccessContext, observedAt time.Time) (EditableSettings, error) {
	if ctx == nil || querier == nil {
		return EditableSettings{}, fmt.Errorf("editable site settings loader is incomplete")
	}
	if !policy.CanAdminister(actor) {
		return EditableSettings{}, ErrDenied
	}
	if observedAt.IsZero() {
		return EditableSettings{}, fmt.Errorf("editable site settings time is invalid")
	}
	if err := ctx.Err(); err != nil {
		return EditableSettings{}, fmt.Errorf("load editable site settings: %w", err)
	}
	row, err := querier.LoadEditableSiteSettings(ctx, db.LoadEditableSiteSettingsParams{
		ActorUserID: actor.UserID,
		ObservedAt:  finiteTime(observedAt),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return EditableSettings{}, ErrDenied
	}
	if err != nil {
		return EditableSettings{}, fmt.Errorf("%w: load editable settings", ErrUnavailable)
	}
	if !row.SettingsPresent {
		return EditableSettings{}, fmt.Errorf("%w: missing site settings", ErrUnavailable)
	}
	shell := ShellPresentation{Name: row.SiteName, Description: row.SiteDescription, Theme: row.BrandTheme}
	if !validShell(shell) || row.AdministrationRevision <= 0 {
		return EditableSettings{}, fmt.Errorf("%w: malformed editable settings", ErrUnavailable)
	}
	if _, validateErr := validateRulesTuple(row.RulesMarkdown, row.RulesHtml, row.RulesRendererVersion); validateErr != nil {
		return EditableSettings{}, fmt.Errorf("%w: malformed editable rules", ErrUnavailable)
	}
	return EditableSettings{
		Shell: shell, RulesMarkdown: row.RulesMarkdown,
		RendererVersion: row.RulesRendererVersion, Revision: row.AdministrationRevision,
	}, nil
}

// UpdateSettings validates and renders before opening one bounded read-
// committed transaction, then changes the singleton and audit atomically.
func UpdateSettings(ctx context.Context, beginner transactionBeginner, clock func() time.Time, actor policy.AccessContext, input SettingsInput, requestID pgtype.UUID) (MutationResult, error) {
	if ctx == nil || beginner == nil || clock == nil {
		return MutationResult{}, fmt.Errorf("site settings mutation boundary is incomplete")
	}
	if !policy.CanAdminister(actor) {
		return MutationResult{}, ErrDenied
	}
	if !validInput(input) || !requestID.Valid || requestID.Bytes == ([16]byte{}) {
		return MutationResult{}, ErrInput
	}
	if err := ctx.Err(); err != nil {
		return MutationResult{}, fmt.Errorf("update site settings: %w", err)
	}
	rulesHTML, rendererVersion, renderErr := renderRules(input.RulesMarkdown)
	if renderErr != nil {
		return MutationResult{}, fmt.Errorf("%w: rules", ErrInput)
	}
	observedAt := clock()
	if observedAt.IsZero() {
		return MutationResult{}, fmt.Errorf("site settings clock returned a zero time")
	}
	observedAt = observedAt.UTC().Truncate(time.Microsecond)
	mutationContext, cancel := context.WithTimeout(ctx, mutationTimeout)
	defer cancel()

	result := MutationResult{}
	err := store.WithinTxOptions(mutationContext, beginner, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(queries *db.Queries) error {
		if _, err := queries.ConfigureAdministrationTransaction(mutationContext); err != nil {
			return fmt.Errorf("configure administration transaction: %w", err)
		}
		lockedActor, err := queries.LockSiteSettingsAdministrator(mutationContext, db.LockSiteSettingsAdministratorParams{
			ActorUserID: actor.UserID,
			ObservedAt:  finiteTime(observedAt),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDenied
		}
		if err != nil || lockedActor != actor.UserID {
			return fmt.Errorf("lock site settings administrator: %w", err)
		}
		current, err := queries.LockSiteSettings(mutationContext)
		if err != nil {
			return fmt.Errorf("%w: lock settings", ErrUnavailable)
		}
		if !validLockedSettings(current) {
			return fmt.Errorf("%w: malformed locked settings", ErrUnavailable)
		}
		if current.AdministrationRevision != input.Revision || current.AdministrationRevision == int64(^uint64(0)>>1) {
			return ErrConflict
		}
		if current.SiteName == input.Name && current.SiteDescription == input.Description &&
			current.BrandTheme == input.Theme && current.RulesMarkdown == input.RulesMarkdown &&
			current.RulesHtml == rulesHTML && current.RulesRendererVersion == rendererVersion {
			return ErrConflict
		}
		changed, err := queries.UpdateSiteSettingsAndAudit(mutationContext, db.UpdateSiteSettingsAndAuditParams{
			SiteName: input.Name, SiteDescription: input.Description, BrandTheme: input.Theme,
			RulesMarkdown: input.RulesMarkdown, RulesHtml: rulesHTML, RulesRendererVersion: rendererVersion,
			ObservedAt: finiteTime(observedAt), ExpectedRevision: input.Revision,
			ActorUserID:      pgtype.Int8{Int64: actor.UserID, Valid: true},
			Reason:           pgtype.Text{String: input.Reason, Valid: true},
			PreviousSiteName: current.SiteName, PreviousSiteDescription: current.SiteDescription,
			PreviousBrandTheme: current.BrandTheme, PreviousRulesRendererVersion: current.RulesRendererVersion,
			PreviousRulesMarkdownSha256: digest(current.RulesMarkdown), PreviousRulesHtmlSha256: digest(current.RulesHtml),
			RulesMarkdownSha256: digest(input.RulesMarkdown), RulesHtmlSha256: digest(rulesHTML),
			RequestID: requestID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return fmt.Errorf("update site settings and audit: %w", err)
		}
		if changed.AdministrationRevision != input.Revision+1 || changed.AuditID <= 0 {
			return fmt.Errorf("site settings update returned invalid state")
		}
		result = MutationResult{Revision: changed.AdministrationRevision, AuditID: changed.AuditID}
		return nil
	})
	if err != nil {
		return MutationResult{}, fmt.Errorf("site settings transaction: %w", err)
	}
	return result, nil
}

func validInput(input SettingsInput) bool {
	return validName(input.Name) && validDescription(input.Description) && validTheme(input.Theme) &&
		validReason(input.Reason) && input.Revision > 0 && len(input.RulesMarkdown) <= contentrender.MaximumMarkdownBytes && utf8.ValidString(input.RulesMarkdown)
}

func validShell(shell ShellPresentation) bool {
	return validName(shell.Name) && validDescription(shell.Description) && validTheme(shell.Theme)
}

func validName(value string) bool {
	return canonicalText(value, 80, false)
}

func validDescription(value string) bool {
	return canonicalText(value, 280, true)
}

func canonicalText(value string, maximumRunes int, empty bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maximumRunes || !empty && value == "" || strings.TrimSpace(value) != value || !norm.NFC.IsNormalString(value) {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}

func validTheme(value string) bool {
	switch value {
	case "blue", "cyan", "emerald", "amber", "rose":
		return true
	default:
		return false
	}
}

func validReason(value string) bool {
	return len(value) >= 1 && len(value) <= 2_000 && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || r == '\n' || r == '\r' }) < 0
}

func renderRules(source string) (string, string, error) {
	if source == "" {
		return "", contentrender.RendererVersion, nil
	}
	rendered, err := contentrender.RenderMarkdown(source)
	if err != nil {
		return "", "", err
	}
	return rendered.PersistenceValues()
}

func validateRulesTuple(source, persistedHTML, version string) (contentrender.TrustedHTML, error) {
	if version != contentrender.RendererVersion {
		return contentrender.TrustedHTML{}, fmt.Errorf("rules renderer is stale")
	}
	if source == "" {
		if persistedHTML != "" {
			return contentrender.TrustedHTML{}, fmt.Errorf("empty rules tuple is inconsistent")
		}
		return contentrender.TrustedHTML{}, nil
	}
	rendered, err := contentrender.RenderMarkdown(source)
	if err != nil {
		return contentrender.TrustedHTML{}, err
	}
	wantHTML, wantVersion, err := rendered.PersistenceValues()
	if err != nil || wantVersion != version || wantHTML != persistedHTML {
		return contentrender.TrustedHTML{}, fmt.Errorf("persisted rules do not match renderer")
	}
	return rendered.TrustedHTML(), nil
}

func validLockedSettings(row db.LockSiteSettingsRow) bool {
	if !validShell(ShellPresentation{Name: row.SiteName, Description: row.SiteDescription, Theme: row.BrandTheme}) ||
		row.AdministrationRevision <= 0 || !row.UpdatedAt.Valid || row.UpdatedAt.InfinityModifier != pgtype.Finite {
		return false
	}
	_, err := validateRulesTuple(row.RulesMarkdown, row.RulesHtml, row.RulesRendererVersion)
	return err == nil
}

func finiteTime(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC().Truncate(time.Microsecond), Valid: true}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
