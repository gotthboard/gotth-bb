package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gotthboard/gotth-bb/internal/buildinfo"
	"github.com/gotthboard/gotth-bb/internal/config"
	"github.com/gotthboard/gotth-bb/internal/governance"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const operatorConnectionCloseTimeout = 5 * time.Second

type operatorConnection interface {
	Begin(context.Context) (pgx.Tx, error)
	Close(context.Context) error
}

type connectionFactory func(context.Context, *pgx.ConnConfig) (operatorConnection, error)
type administratorBootstrapper func(
	context.Context,
	operatorConnection,
	func() time.Time,
	string,
	string,
	string,
	pgtype.UUID,
) (governance.BootstrapResult, error)
type identityRebinder func(
	context.Context,
	operatorConnection,
	func() time.Time,
	string,
	string,
	string,
	string,
	string,
	pgtype.UUID,
) (governance.IdentityRebindResult, error)

// main binds process cancellation and production dependencies to the tested
// one-shot operator runner. It prints one bounded failure and exits nonzero.
//
// Complexity: local time and auxiliary space are tight Theta(1); argument,
// database, entropy, transaction, and output costs are delegated to run.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runOperator(
		ctx,
		os.LookupEnv,
		os.Args[1:],
		os.Stdout,
		rand.Reader,
		time.Now,
		func(connectContext context.Context, configured *pgx.ConnConfig) (operatorConnection, error) {
			return pgx.ConnectConfig(connectContext, configured)
		},
		func(
			bootstrapContext context.Context,
			database operatorConnection,
			clock func() time.Time,
			issuer string,
			subject string,
			operatorIdentifier string,
			requestID pgtype.UUID,
		) (governance.BootstrapResult, error) {
			return governance.BootstrapAdministrator(
				bootstrapContext, database, clock, issuer, subject, operatorIdentifier, requestID,
			)
		},
		func(
			rebindContext context.Context,
			database operatorConnection,
			clock func() time.Time,
			oldIssuer string,
			oldSubject string,
			newIssuer string,
			newSubject string,
			operatorIdentifier string,
			requestID pgtype.UUID,
		) (governance.IdentityRebindResult, error) {
			return governance.RebindExternalIdentity(
				rebindContext, database, clock,
				oldIssuer, oldSubject, newIssuer, newSubject, operatorIdentifier, requestID,
			)
		},
	); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gotth-bb-operator: %v\n", err)
		os.Exit(1)
	}
}

// run preserves the focused bootstrap runner seam used by its exhaustive unit
// tests. Production dispatch goes through runOperator, which also admits the
// stopped-application identity cutover command.
func run(
	ctx context.Context,
	lookup config.LookupEnv,
	args []string,
	output io.Writer,
	entropy io.Reader,
	clock func() time.Time,
	connect connectionFactory,
	bootstrap administratorBootstrapper,
) error {
	return runOperator(ctx, lookup, args, output, entropy, clock, connect, bootstrap, nil)
}

// runOperator parses one explicit operator command, loads only database
// configuration, creates an RFC 4122 version 4 audit request ID, opens one
// direct PostgreSQL connection, performs one non-retried governed action, and
// reports IDs only after the transaction has committed. Connection errors are
// redacted; a post-commit output failure is labeled as committed.
//
// Complexity: for a argument bytes, n database-URL bytes, delegated parse
// costs P(n)/S(n), connection work C/A(C), transaction work B/A(B), and output
// work W/A(W), time is O(a+P(n)+C+B+W), Omega(1), with no tight Theta bound
// because delegated runtime costs vary. Auxiliary space is
// O(a+S(n)+A(C)+A(B)+A(W)), Omega(1), with no tight Theta bound established.
// Request-ID entropy and local ownership transitions are tight Theta(1).
func runOperator(
	ctx context.Context,
	lookup config.LookupEnv,
	args []string,
	output io.Writer,
	entropy io.Reader,
	clock func() time.Time,
	connect connectionFactory,
	bootstrap administratorBootstrapper,
	rebind identityRebinder,
) error {
	if ctx == nil {
		return fmt.Errorf("operator command context is required")
	}
	if lookup == nil {
		return fmt.Errorf("operator configuration lookup is required")
	}
	if args == nil {
		return fmt.Errorf("operator command arguments are required")
	}
	if output == nil {
		return fmt.Errorf("operator command output is required")
	}
	if entropy == nil {
		return fmt.Errorf("operator request ID entropy is required")
	}
	if clock == nil {
		return fmt.Errorf("operator command clock is required")
	}
	if connect == nil {
		return fmt.Errorf("operator database connector is required")
	}
	if bootstrap == nil {
		return fmt.Errorf("administrator bootstrapper is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("operator command canceled: %w", err)
	}
	if len(args) == 1 && args[0] == "version" {
		release, releaseErr := buildinfo.Current()
		if releaseErr != nil {
			return fmt.Errorf("load release identity: %w", releaseErr)
		}
		if _, writeErr := fmt.Fprintf(output, "gotth-bb version=%s commit=%s\n", release.Version, release.Commit); writeErr != nil {
			return fmt.Errorf("write release identity: %w", writeErr)
		}
		return nil
	}
	if len(args) == 0 {
		return fmt.Errorf("expected bootstrap-administrator or rebind-external-identity command")
	}
	command := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var issuer, subject, oldIssuer, oldSubject, newIssuer, newSubject, operatorIdentifier string
	seenFlags := make(map[string]bool, 5)
	bindFlag := func(name string, target *string) {
		flags.Func(name, "required exact value", func(value string) error {
			if seenFlags[name] {
				return fmt.Errorf("duplicate flag")
			}
			seenFlags[name] = true
			*target = value
			return nil
		})
	}
	switch command {
	case "bootstrap-administrator":
		bindFlag("issuer", &issuer)
		bindFlag("subject", &subject)
		bindFlag("operator", &operatorIdentifier)
	case "rebind-external-identity":
		if rebind == nil {
			return fmt.Errorf("external identity rebinder is required")
		}
		bindFlag("old-issuer", &oldIssuer)
		bindFlag("old-subject", &oldSubject)
		bindFlag("new-issuer", &newIssuer)
		bindFlag("new-subject", &newSubject)
		bindFlag("operator", &operatorIdentifier)
	default:
		return fmt.Errorf("expected bootstrap-administrator or rebind-external-identity command")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return fmt.Errorf("invalid %s arguments", command)
	}
	if command == "bootstrap-administrator" &&
		(flags.NArg() != 0 || issuer == "" || subject == "" || operatorIdentifier == "") {
		return fmt.Errorf("bootstrap-administrator requires exactly --issuer, --subject, and --operator")
	}
	if command == "rebind-external-identity" &&
		(flags.NArg() != 0 || oldIssuer == "" || oldSubject == "" || newIssuer == "" || newSubject == "" || operatorIdentifier == "") {
		return fmt.Errorf("rebind-external-identity requires exactly --old-issuer, --old-subject, --new-issuer, --new-subject, and --operator")
	}
	configured, err := config.LoadDatabaseConnectionConfig(lookup)
	if err != nil {
		return fmt.Errorf("load operator database configuration: %w", err)
	}
	var requestBytes [16]byte
	defer clear(requestBytes[:])
	if _, err := io.ReadFull(entropy, requestBytes[:]); err != nil {
		return fmt.Errorf("read operator request ID entropy: %w", err)
	}
	requestBytes[6] = requestBytes[6]&0x0f | 0x40
	requestBytes[8] = requestBytes[8]&0x3f | 0x80
	requestID := pgtype.UUID{Bytes: requestBytes, Valid: true}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("operator command canceled: %w", err)
	}
	database, err := connect(ctx, configured)
	if err != nil {
		if database != nil {
			closeContext, cancel := context.WithTimeout(context.Background(), operatorConnectionCloseTimeout)
			_ = database.Close(closeContext)
			cancel()
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return fmt.Errorf("connect operator database: %w", contextErr)
		}
		return fmt.Errorf("connect operator database failed")
	}
	if database == nil {
		return fmt.Errorf("connect operator database returned no connection")
	}
	defer func() {
		closeContext, cancel := context.WithTimeout(context.Background(), operatorConnectionCloseTimeout)
		_ = database.Close(closeContext)
		cancel()
	}()
	if command == "bootstrap-administrator" {
		result, err := bootstrap(ctx, database, clock, issuer, subject, operatorIdentifier, requestID)
		if err != nil {
			return fmt.Errorf("bootstrap administrator: %w", err)
		}
		if result.UserID <= 0 || result.AuditID <= 0 {
			return fmt.Errorf("bootstrap administrator returned an invalid committed result")
		}
		if _, err := fmt.Fprintf(output, "administrator bootstrap committed: user_id=%d audit_id=%d\n", result.UserID, result.AuditID); err != nil {
			return fmt.Errorf("administrator bootstrap committed but result output failed: %w", err)
		}
		return nil
	}
	result, err := rebind(
		ctx, database, clock, oldIssuer, oldSubject, newIssuer, newSubject, operatorIdentifier, requestID,
	)
	if err != nil {
		return fmt.Errorf("rebind external identity: %w", err)
	}
	if result.UserID <= 0 || result.AuditID <= 0 || result.RevokedSessions < 0 || result.DiscardedLoginAttempts < 0 {
		return fmt.Errorf("external identity rebind returned an invalid committed result")
	}
	if _, err := fmt.Fprintf(
		output,
		"external identity rebind committed: user_id=%d audit_id=%d revoked_sessions=%d discarded_login_attempts=%d\n",
		result.UserID, result.AuditID, result.RevokedSessions, result.DiscardedLoginAttempts,
	); err != nil {
		return fmt.Errorf("external identity rebind committed but result output failed: %w", err)
	}
	return nil
}
