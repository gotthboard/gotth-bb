package httpui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/site"
	"github.com/jackc/pgx/v5/pgtype"
)

const maximumSiteSettingsFormBytes = 262_144

type SiteHTTPServices struct {
	Shell    func(context.Context) (site.ShellPresentation, error)
	Rules    func(context.Context) (site.PublicRules, error)
	Editable func(context.Context, auth.AccessContext) (site.EditableSettings, error)
	Update   func(context.Context, auth.AccessContext, site.SettingsInput, pgtype.UUID) (site.MutationResult, error)
}

func newSiteSettingsHandler(builder URLBuilder, services SiteHTTPServices) (http.Handler, http.Handler, error) {
	if services.Shell == nil || services.Rules == nil || services.Editable == nil || services.Update == nil {
		return nil, nil, fmt.Errorf("browser site settings services are incomplete")
	}
	settingsURL, err := builder.Path("admin", "settings")
	if err != nil {
		return nil, nil, fmt.Errorf("build site settings URL: %w", err)
	}
	loginURL, err := builder.PathWithQuery([]string{"login"}, url.Values{"return": {settingsURL}})
	if err != nil {
		return nil, nil, fmt.Errorf("build site settings login URL: %w", err)
	}
	revalidationURL, err := builder.PathWithQuery([]string{"auth", "revalidate"}, url.Values{"return": {settingsURL}})
	if err != nil {
		return nil, nil, fmt.Errorf("build site settings revalidation URL: %w", err)
	}
	rulesView, err := newPageView(builder, "Rules", "rules")
	if err != nil {
		return nil, nil, fmt.Errorf("construct rules view: %w", err)
	}
	settingsView, err := newPageView(builder, "Site settings", "admin", "settings")
	if err != nil {
		return nil, nil, fmt.Errorf("construct site settings view: %w", err)
	}

	publicRouter := chi.NewRouter()
	publicRouter.Use(captureRoutePattern)
	publicRouter.Get("/rules", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		if request.URL.RawPath != "" || request.URL.RawQuery != "" || request.URL.ForceQuery {
			if renderErr := renderResponse(response, request, http.StatusNotFound,
				errorPage(rulesView, http.StatusNotFound, "Page not found", "The requested rules page does not exist."),
				errorContent(rulesView, http.StatusNotFound, "Page not found", "The requested rules page does not exist.")); renderErr != nil {
				panic(renderErr)
			}
			return
		}
		rules, loadErr := services.Rules(request.Context())
		if loadErr != nil {
			if renderErr := renderUnbrandedShellFailure(response, http.StatusServiceUnavailable); renderErr != nil {
				panic(renderErr)
			}
			return
		}
		request = request.WithContext(withSiteShell(request.Context(), rules.Shell))
		presentation := publicRulesPageView{HTML: rules.HTML, Empty: rules.HTML == (site.PublicRules{}).HTML}
		if renderErr := renderResponse(response, request, http.StatusOK, publicRulesPage(rulesView, presentation), publicRulesContent(rulesView, presentation)); renderErr != nil {
			panic(renderErr)
		}
	})

	authorized := func(request *http.Request) (auth.AccessContext, string, bool) {
		authentication := sessionAuthenticationFromContext(request.Context())
		if !authentication.Access.Authenticated || authentication.SessionID <= 0 {
			return auth.AccessContext{}, loginURL, false
		}
		if authentication.RequiresRevalidation {
			return auth.AccessContext{}, revalidationURL, false
		}
		if !policy.CanAdminister(authentication.Access) {
			return authentication.Access, "", false
		}
		return authentication.Access, "", true
	}
	renderSettings := func(response http.ResponseWriter, request *http.Request, status int, actor auth.AccessContext, formError string) {
		settings, loadErr := services.Editable(request.Context(), actor)
		if loadErr != nil {
			if errors.Is(loadErr, site.ErrDenied) {
				if renderErr := renderResponse(response, request, http.StatusForbidden,
					errorPage(settingsView, http.StatusForbidden, "Administration denied", "Your current account cannot administer site settings."),
					errorContent(settingsView, http.StatusForbidden, "Administration denied", "Your current account cannot administer site settings.")); renderErr != nil {
					panic(renderErr)
				}
				return
			}
			if renderErr := renderUnbrandedShellFailure(response, http.StatusServiceUnavailable); renderErr != nil {
				panic(renderErr)
			}
			return
		}
		request = request.WithContext(withSiteShell(request.Context(), settings.Shell))
		presentation := siteSettingsPageView{
			ActionURL: settingsURL, CSRFToken: csrfTokenFromContext(request.Context()),
			SiteName: settings.Shell.Name, SiteDescription: settings.Shell.Description, BrandTheme: settings.Shell.Theme,
			RulesMarkdown: settings.RulesMarkdown, Revision: strconv.FormatInt(settings.Revision, 10), FormError: formError,
		}
		if renderErr := renderResponse(response, request, status, siteSettingsPage(settingsView, presentation), siteSettingsContent(settingsView, presentation)); renderErr != nil {
			panic(renderErr)
		}
	}

	privateRouter := chi.NewRouter()
	privateRouter.Use(captureRoutePattern)
	privateRouter.Get("/admin/settings", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		actor, redirect, allowed := authorized(request)
		if redirect != "" {
			serveSessionRedirect(response, request, redirect)
			return
		}
		if !allowed {
			renderSettings(response, request, http.StatusForbidden, actor, "")
			return
		}
		if request.URL.RawPath != "" || request.URL.RawQuery != "" || request.URL.ForceQuery {
			if renderErr := renderResponse(response, request, http.StatusNotFound,
				errorPage(settingsView, http.StatusNotFound, "Page not found", "The requested administration page does not exist."),
				errorContent(settingsView, http.StatusNotFound, "Page not found", "The requested administration page does not exist.")); renderErr != nil {
				panic(renderErr)
			}
			return
		}
		renderSettings(response, request, http.StatusOK, actor, "")
	})
	privateRouter.Post("/admin/settings", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		actor, redirect, allowed := authorized(request)
		if redirect != "" {
			serveSessionRedirect(response, request, redirect)
			return
		}
		if !allowed {
			renderSettings(response, request, http.StatusForbidden, actor, "")
			return
		}
		if request.URL.RawPath != "" || request.URL.RawQuery != "" || request.URL.ForceQuery {
			if renderErr := renderResponse(response, request, http.StatusNotFound,
				errorPage(settingsView, http.StatusNotFound, "Page not found", "The requested administration action does not exist."),
				errorContent(settingsView, http.StatusNotFound, "Page not found", "The requested administration action does not exist.")); renderErr != nil {
				panic(renderErr)
			}
			return
		}
		if csrfErr := validateCSRFRequest(request, maximumSiteSettingsFormBytes); csrfErr != nil {
			renderSettings(response, request, http.StatusForbidden, actor, "Reload site settings and try again.")
			return
		}
		input, parseErr := parseSiteSettingsForm(request)
		if parseErr != nil {
			renderSettings(response, request, http.StatusBadRequest, actor, "The site settings form is invalid. Reload it and try again.")
			return
		}
		requestID, requestIDErr := moderationRequestUUID(request.Context())
		if requestIDErr != nil {
			if renderErr := renderUnbrandedShellFailure(response, http.StatusServiceUnavailable); renderErr != nil {
				panic(renderErr)
			}
			return
		}
		result, updateErr := services.Update(request.Context(), actor, input, requestID)
		if updateErr != nil {
			switch {
			case errors.Is(updateErr, site.ErrInput):
				renderSettings(response, request, http.StatusUnprocessableEntity, actor, "Check every field. Text must be canonical, the theme must be listed, and the audit reason must be one line.")
			case errors.Is(updateErr, site.ErrDenied):
				renderSettings(response, request, http.StatusForbidden, actor, "")
			case errors.Is(updateErr, site.ErrConflict):
				renderSettings(response, request, http.StatusConflict, actor, "The settings changed or did not differ. Review the current values and try again.")
			default:
				if renderErr := renderUnbrandedShellFailure(response, http.StatusServiceUnavailable); renderErr != nil {
					panic(renderErr)
				}
			}
			return
		}
		if result.Revision != input.Revision+1 || result.AuditID <= 0 {
			if renderErr := renderUnbrandedShellFailure(response, http.StatusServiceUnavailable); renderErr != nil {
				panic(renderErr)
			}
			return
		}
		serveSessionRedirect(response, request, settingsURL)
	})
	return recordRoutePattern(publicRouter), recordRoutePattern(privateRouter), nil
}

// withExactSiteRoutePreflight rejects noncanonical site routes before session,
// body, or database work. The outer shell loader is lazy, so this fixed
// response also performs no presentation query.
func withExactSiteRoutePreflight(next http.Handler, path string, methods ...string) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		patternMethod := "*"
		for _, method := range methods {
			if request.Method == method {
				patternMethod = method
				break
			}
		}
		request.Pattern = patternMethod + " " + path
		if request.URL.Path != path || request.URL.RawPath != "" || request.URL.RawQuery != "" || request.URL.ForceQuery {
			if err := renderUnbrandedRouteError(response, http.StatusNotFound); err != nil {
				panic(err)
			}
			return
		}
		for _, method := range methods {
			if patternMethod == method {
				next.ServeHTTP(response, request)
				return
			}
		}
		response.Header().Set("Allow", strings.Join(methods, ", "))
		if err := renderUnbrandedRouteError(response, http.StatusMethodNotAllowed); err != nil {
			panic(err)
		}
	})
}

func parseSiteSettingsForm(request *http.Request) (site.SettingsInput, error) {
	if err := request.ParseForm(); err != nil {
		return site.SettingsInput{}, fmt.Errorf("parse site settings form: %w", err)
	}
	allowed := map[string]bool{"_csrf": true, "site_name": true, "site_description": true, "brand_theme": true, "rules_markdown": true, "reason": true, "revision": true}
	for key, values := range request.PostForm {
		if !allowed[key] || len(values) != 1 {
			return site.SettingsInput{}, fmt.Errorf("site settings form field is missing, duplicated, or unknown")
		}
	}
	for key := range allowed {
		if len(request.PostForm[key]) != 1 {
			return site.SettingsInput{}, fmt.Errorf("site settings form field is missing, duplicated, or unknown")
		}
	}
	revision, err := parseCanonicalPositiveID(request.PostForm.Get("revision"))
	if err != nil {
		return site.SettingsInput{}, fmt.Errorf("site settings revision is invalid")
	}
	return site.SettingsInput{
		Name: request.PostForm.Get("site_name"), Description: request.PostForm.Get("site_description"),
		Theme: request.PostForm.Get("brand_theme"), RulesMarkdown: request.PostForm.Get("rules_markdown"),
		Reason: request.PostForm.Get("reason"), Revision: revision,
	}, nil
}
