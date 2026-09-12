package httpui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"

	"github.com/gotthboard/gotth-bb/internal/control"
)

var registrationFlowSlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type RegistrationHTTPServices struct {
	LoadSettings     func(context.Context) (control.Settings, error)
	Issuer           url.URL
	OpenFlowSlug     string
	ApprovalFlowSlug string
	SMTPConfigured   bool
}

type registrationPageView struct {
	Heading, Message, FlowURL, LinkLabel string
}

func newDynamicRegistrationHandler(builder URLBuilder, services RegistrationHTTPServices) (http.Handler, error) {
	if services.LoadSettings == nil || !validRegistrationIssuer(services.Issuer) || !registrationFlowSlug.MatchString(services.OpenFlowSlug) || !registrationFlowSlug.MatchString(services.ApprovalFlowSlug) || services.OpenFlowSlug == services.ApprovalFlowSlug {
		return nil, fmt.Errorf("dynamic registration services are invalid")
	}
	view, err := newPageView(builder, "Registration", "register")
	if err != nil {
		return nil, err
	}
	loginURL, err := builder.Absolute("login")
	if err != nil {
		return nil, err
	}
	flowURL := func(slug string) string {
		target := url.URL{Scheme: services.Issuer.Scheme, Host: services.Issuer.Host, Path: "/if/flow/" + slug + "/"}
		target.RawQuery = url.Values{"next": {loginURL}}.Encode()
		return target.String()
	}
	openURL, approvalURL := flowURL(services.OpenFlowSlug), flowURL(services.ApprovalFlowSlug)
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Pragma", "no-cache")
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			renderRegistration(response, request, view, http.StatusMethodNotAllowed, registrationPageView{Heading: "Method not allowed", Message: "Use the registration page."})
			return
		}
		if request.URL.RawPath != "" || request.URL.RawQuery != "" || request.URL.ForceQuery {
			renderRegistration(response, request, view, http.StatusNotFound, registrationPageView{Heading: "Page not found", Message: "The requested registration page does not exist."})
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), registrationAdmissionTimeout)
		defer cancel()
		settings, loadErr := services.LoadSettings(ctx)
		if loadErr != nil || !settings.Registration.Valid() || settings.MaintenanceEnabled {
			renderRegistration(response, request, view, http.StatusServiceUnavailable, registrationPageView{Heading: "Registration unavailable", Message: "Registration is temporarily unavailable."})
			return
		}
		presentation := registrationPageView{Heading: "Registration"}
		switch settings.Registration {
		case control.RegistrationClosed:
			presentation.Message = "Registration is currently closed."
		case control.RegistrationInvitationOnly:
			presentation.Message = "Registration is by invitation only. Use the private link you received."
		case control.RegistrationVerifiedEmailOpen:
			if !services.SMTPConfigured {
				renderRegistration(response, request, view, http.StatusServiceUnavailable, registrationPageView{Heading: "Registration unavailable", Message: "Registration is temporarily unavailable."})
				return
			}
			presentation.Message, presentation.FlowURL, presentation.LinkLabel = "Create an account after verifying your email address.", openURL, "Continue to registration"
		case control.RegistrationAdministratorApproval:
			if !services.SMTPConfigured {
				renderRegistration(response, request, view, http.StatusServiceUnavailable, registrationPageView{Heading: "Registration unavailable", Message: "Registration is temporarily unavailable."})
				return
			}
			presentation.Message, presentation.FlowURL, presentation.LinkLabel = "Verify your email and submit an application for administrator approval.", approvalURL, "Submit an application"
		default:
			renderRegistration(response, request, view, http.StatusServiceUnavailable, registrationPageView{Heading: "Registration unavailable", Message: "Registration is temporarily unavailable."})
			return
		}
		renderRegistration(response, request, view, http.StatusOK, presentation)
	}), nil
}

func validRegistrationIssuer(issuer url.URL) bool {
	if issuer.Host == "" || issuer.Hostname() == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return false
	}
	if issuer.Scheme == "https" {
		return true
	}
	return issuer.Scheme == "http" && (issuer.Hostname() == "127.0.0.1" || issuer.Hostname() == "localhost")
}

func renderRegistration(response http.ResponseWriter, request *http.Request, view pageView, status int, presentation registrationPageView) {
	if status != http.StatusOK {
		view.CanonicalURL = ""
	}
	if err := renderResponse(response, request, status, registrationPage(view, presentation), registrationContent(presentation)); err != nil {
		panic(err)
	}
}
