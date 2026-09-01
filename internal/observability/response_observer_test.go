package observability

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

type statusSequenceWriter struct {
	header   http.Header
	statuses []int
	body     bytes.Buffer
}

func (writer *statusSequenceWriter) Header() http.Header {
	return writer.header
}

func (writer *statusSequenceWriter) Write(payload []byte) (int, error) {
	return writer.body.Write(payload)
}

func (writer *statusSequenceWriter) WriteHeader(status int) {
	if status < 100 || status > 999 {
		panic("invalid status")
	}
	writer.statuses = append(writer.statuses, status)
}

func TestResponseObserverWriteHeaderRecordsFirstStatus(t *testing.T) {
	t.Parallel()

	underlying := httptest.NewRecorder()
	observer := &responseObserver{ResponseWriter: underlying}
	observer.WriteHeader(http.StatusCreated)
	observer.WriteHeader(http.StatusInternalServerError)

	if !observer.wroteHeader || observer.status != http.StatusCreated {
		t.Fatalf("observer state = wrote %v, status %d", observer.wroteHeader, observer.status)
	}
	if underlying.Code != http.StatusCreated {
		t.Fatalf("underlying status = %d", underlying.Code)
	}
}

func TestResponseObserverWriteHeaderDoesNotRecordRejectedStatus(t *testing.T) {
	t.Parallel()

	underlying := httptest.NewRecorder()
	observer := &responseObserver{ResponseWriter: underlying}
	recovered := capturePanic(func() {
		observer.WriteHeader(99)
	})

	if recovered == nil {
		t.Fatal("WriteHeader(99) did not panic")
	}
	if observer.wroteHeader || observer.status != 0 {
		t.Fatalf("observer recorded rejected status: wrote %v, status %d", observer.wroteHeader, observer.status)
	}
}

func TestResponseObserverWriteHeaderForwardsInformationalBeforeFinalStatus(t *testing.T) {
	t.Parallel()

	underlying := &statusSequenceWriter{header: make(http.Header)}
	observer := &responseObserver{ResponseWriter: underlying}
	observer.WriteHeader(http.StatusEarlyHints)
	if observer.wroteHeader || observer.status != 0 {
		t.Fatalf("informational status recorded as final: wrote %v, status %d", observer.wroteHeader, observer.status)
	}
	observer.WriteHeader(http.StatusNotFound)
	observer.WriteHeader(http.StatusInternalServerError)

	if !observer.wroteHeader || observer.status != http.StatusNotFound {
		t.Fatalf("observer state = wrote %v, status %d", observer.wroteHeader, observer.status)
	}
	if got := fmt.Sprint(underlying.statuses); got != "[103 404]" {
		t.Fatalf("forwarded statuses = %s", got)
	}
}

func TestResponseObserverTreatsSwitchingProtocolsAsFinal(t *testing.T) {
	t.Parallel()

	underlying := &statusSequenceWriter{header: make(http.Header)}
	observer := &responseObserver{ResponseWriter: underlying}
	observer.WriteHeader(http.StatusSwitchingProtocols)

	if !observer.wroteHeader || observer.status != http.StatusSwitchingProtocols {
		t.Fatalf("observer state = wrote %v, status %d", observer.wroteHeader, observer.status)
	}
}

func TestResponseObserverWriteRecordsImplicitStatusAndBytes(t *testing.T) {
	t.Parallel()

	underlying := httptest.NewRecorder()
	observer := &responseObserver{ResponseWriter: underlying}
	written, err := observer.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write() returned error: %v", err)
	}
	if written != 5 || observer.bytes != 5 {
		t.Fatalf("written = %d, observed bytes = %d", written, observer.bytes)
	}
	if !observer.wroteHeader || observer.status != http.StatusOK {
		t.Fatalf("observer state = wrote %v, status %d", observer.wroteHeader, observer.status)
	}
	if underlying.Body.String() != "hello" {
		t.Fatalf("underlying body = %q", underlying.Body.String())
	}
}

func TestResponseObserverUnwrap(t *testing.T) {
	t.Parallel()

	underlying := httptest.NewRecorder()
	observer := &responseObserver{ResponseWriter: underlying}
	if got := observer.Unwrap(); got != underlying {
		t.Fatalf("Unwrap() = %T, want underlying writer", got)
	}
}
