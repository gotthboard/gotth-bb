package httpui

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/gotthboard/gotth-bb/internal/control"
	"github.com/gotthboard/gotth-bb/internal/registration"
)

const (
	registrationAdmissionTimeout = 500 * time.Millisecond
	maximumApprovalIntakeBytes   = 8 << 10
)

type RegistrationControlHTTPServices struct {
	LoadSettings   func(context.Context) (control.Settings, error)
	VerifyApproval func(context.Context, string) (registration.Intake, error)
	AcceptApproval func(context.Context, registration.Intake) error
	SMTPConfigured bool
}

// NewRegistrationControlHandler installs only the public admission and signed
// approval-intake machine endpoints. Other requests pass through unchanged.
func NewRegistrationControlHandler(next http.Handler, services RegistrationControlHTTPServices) (http.Handler, error) {
	if next == nil || services.LoadSettings == nil || services.VerifyApproval == nil || services.AcceptApproval == nil {
		return nil, fmt.Errorf("registration control HTTP services are incomplete")
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.RawPath != "" {
			next.ServeHTTP(response, request)
			return
		}
		if mode, matched := strings.CutPrefix(request.URL.Path, "/registration/admission/"); matched && mode != "" && !strings.ContainsRune(mode, '/') {
			request.Pattern = request.Method + " /registration/admission/{mode}"
			serveRegistrationAdmission(response, request, mode, services)
			return
		}
		if request.URL.Path == "/registration/intake/approval" {
			request.Pattern = request.Method + " /registration/intake/approval"
			serveApprovalIntake(response, request, services)
			return
		}
		next.ServeHTTP(response, request)
	}), nil
}

func serveRegistrationAdmission(response http.ResponseWriter, request *http.Request, rawMode string, services RegistrationControlHTTPServices) {
	response.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if request.URL.RawQuery != "" || request.Body != nil && request.Body != http.NoBody {
		response.WriteHeader(http.StatusNotFound)
		return
	}
	mode := control.RegistrationMode(rawMode)
	if mode != control.RegistrationVerifiedEmailOpen && mode != control.RegistrationAdministratorApproval && mode != control.RegistrationInvitationOnly {
		response.WriteHeader(http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), registrationAdmissionTimeout)
	defer cancel()
	settings, err := services.LoadSettings(ctx)
	if err != nil || !settings.Registration.Valid() {
		response.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if settings.MaintenanceEnabled || settings.Registration != mode || !services.SMTPConfigured {
		response.WriteHeader(http.StatusNotFound)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func serveApprovalIntake(response http.ResponseWriter, request *http.Request, services RegistrationControlHTTPServices) {
	response.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if request.URL.RawQuery != "" {
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/jwt" || len(parameters) != 0 || request.Body == nil {
		response.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	if request.ContentLength > maximumApprovalIntakeBytes {
		response.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maximumApprovalIntakeBytes+1))
	if err != nil || len(raw) > maximumApprovalIntakeBytes {
		response.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	intake, err := services.VerifyApproval(request.Context(), string(raw))
	if err != nil {
		response.WriteHeader(http.StatusAccepted)
		return
	}
	if err := services.AcceptApproval(request.Context(), intake); err != nil {
		response.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	response.WriteHeader(http.StatusAccepted)
}
