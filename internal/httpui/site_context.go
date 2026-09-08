package httpui

import (
	"context"
	"net/http"

	"github.com/gotthboard/gotth-bb/internal/site"
)

type siteShellLoaderContextKey struct{}
type siteShellContextKey struct{}

type siteShellLoader func(context.Context) (site.ShellPresentation, error)

func withSiteShellLoader(next http.Handler, load siteShellLoader) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		ctx := context.WithValue(request.Context(), siteShellLoaderContextKey{}, load)
		contextualRequest := request.WithContext(ctx)
		defer func() { request.Pattern = contextualRequest.Pattern }()
		next.ServeHTTP(response, contextualRequest)
	})
}

func withSiteShell(ctx context.Context, shell site.ShellPresentation) context.Context {
	return context.WithValue(ctx, siteShellContextKey{}, shell)
}

func loadSiteShellForDocument(ctx context.Context) (context.Context, error) {
	if _, ok := ctx.Value(siteShellContextKey{}).(site.ShellPresentation); ok {
		return ctx, nil
	}
	load, ok := ctx.Value(siteShellLoaderContextKey{}).(siteShellLoader)
	if !ok || load == nil {
		return ctx, nil
	}
	shell, err := load(ctx)
	if err != nil {
		return ctx, err
	}
	return withSiteShell(ctx, shell), nil
}

func siteShellFromContext(ctx context.Context, fallback pageView) site.ShellPresentation {
	if shell, ok := ctx.Value(siteShellContextKey{}).(site.ShellPresentation); ok {
		return shell
	}
	return site.ShellPresentation{Name: fallback.SiteName, Description: fallback.SiteDescription, Theme: fallback.BrandTheme}
}
