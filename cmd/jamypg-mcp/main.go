package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"jamypg/internal/catalog"
	"jamypg/internal/mcp"
	"jamypg/internal/meta"
)

func main() {
	var (
		dataDir        string
		transport      string
		addr           string
		endpoint       string
		allowOrigins   string
		publicMCP      bool
		stateless      bool
		ssePost        bool
		adminToken     string
		feedbackTenant string
		metaDSN        string
		bootstrapAdmin string
		oidcIssuer     string
		oidcClientID   string
		oidcSecret     string
		oidcRedirect   string
		syncSource     string
		syncInterval   time.Duration
		syncApply      bool
		digestWebhook  string
		omURL          string
		omToken        string
		omSync         bool
		omScope        string
		dbaDigest      bool
		dbaDigestProf  string
	)
	flag.StringVar(&dataDir, "data", filepath.Join("data", "metadb"), "Path to metadata dataset directory")
	flag.StringVar(&transport, "transport", "http", "MCP transport: http or stdio")
	flag.StringVar(&addr, "addr", "127.0.0.1:9797", "HTTP listen address")
	flag.StringVar(&endpoint, "endpoint", "/mcp", "MCP endpoint path")
	flag.StringVar(&allowOrigins, "allow-origin", "", "Comma-separated additional allowed Origin values")
	flag.BoolVar(&publicMCP, "public-mcp", false, "Allow standalone HTTP MCP on a non-loopback listen address (explicit security opt-in)")
	flag.BoolVar(&stateless, "stateless", false, "Disable Mcp-Session-Id session management")
	flag.BoolVar(&ssePost, "sse-post", false, "Return POST responses as text/event-stream instead of application/json")
	flag.StringVar(&adminToken, "admin-token", os.Getenv("JAMYPG_ADMIN_TOKEN"), "Master token for admin access (default: JAMYPG_ADMIN_TOKEN env)")
	flag.StringVar(&feedbackTenant, "feedback-tenant", os.Getenv("JAMYPG_FEEDBACK_TENANT"), "Server-owned feedback tenant/workspace scope (default: JAMYPG_FEEDBACK_TENANT or default)")
	flag.StringVar(&metaDSN, "meta-db", os.Getenv("JAMYPG_META_DB"), "Postgres DSN for the meta DB; enables login/users/MCP keys/per-user profiles (default: JAMYPG_META_DB env)")
	flag.StringVar(&bootstrapAdmin, "bootstrap-admin", os.Getenv("JAMYPG_BOOTSTRAP_ADMIN"), "Initial admin as username:password when the meta DB is empty (default: JAMYPG_BOOTSTRAP_ADMIN env; empty generates a random password logged once)")
	flag.StringVar(&oidcIssuer, "oidc-issuer", os.Getenv("JAMYPG_OIDC_ISSUER"), "Keycloak realm issuer URL, e.g. https://kc.example.com/realms/myrealm")
	flag.StringVar(&oidcClientID, "oidc-client-id", os.Getenv("JAMYPG_OIDC_CLIENT_ID"), "OIDC client id")
	flag.StringVar(&oidcSecret, "oidc-client-secret", os.Getenv("JAMYPG_OIDC_CLIENT_SECRET"), "OIDC client secret")
	flag.StringVar(&oidcRedirect, "oidc-redirect-url", os.Getenv("JAMYPG_OIDC_REDIRECT_URL"), "OIDC redirect URL, e.g. https://host:9797/auth/sso/callback")
	flag.StringVar(&syncSource, "sync-source", os.Getenv("JAMYPG_SYNC_SOURCE"), "DB profile id to auto-sync metadata from on a schedule (with -sync-interval)")
	flag.DurationVar(&syncInterval, "sync-interval", 0, "Interval for the scheduler (e.g. 24h); 0 disables. Drives -sync-source and/or -digest-webhook")
	flag.BoolVar(&syncApply, "sync-apply", false, "On each scheduled sync, auto-apply the collected physical model to the catalog (retire candidates kept). Requires -sync-source")
	flag.StringVar(&digestWebhook, "digest-webhook", os.Getenv("JAMYPG_DIGEST_WEBHOOK"), "URL to POST the metadata digest JSON to on each scheduler tick (with -sync-interval)")
	flag.BoolVar(&dbaDigest, "dba-digest", false, "On each scheduler tick, also POST the DBA digest (workload + index candidates) to -digest-webhook")
	flag.StringVar(&dbaDigestProf, "dba-digest-profile", os.Getenv("JAMYPG_DBA_DIGEST_PROFILE"), "Optional DB profile id to scope the scheduled DBA digest (empty = all profiles)")
	flag.StringVar(&omURL, "openmetadata-url", os.Getenv("JAMYPG_OPENMETADATA_URL"), "OpenMetadata base URL (e.g. http://host:8585) to import/export metadata")
	flag.StringVar(&omToken, "openmetadata-token", os.Getenv("JAMYPG_OPENMETADATA_TOKEN"), "OpenMetadata bot JWT token (default: JAMYPG_OPENMETADATA_TOKEN env)")
	flag.BoolVar(&omSync, "openmetadata-sync", false, "On each scheduler tick, apply an incremental OpenMetadata import (gaps only). Requires -sync-interval")
	flag.StringVar(&omScope, "openmetadata-scope", os.Getenv("JAMYPG_OPENMETADATA_SCOPE"), "Optional OpenMetadata database/schema FQN to scope scheduled imports")
	flag.Parse()

	if err := validateHTTPExposure(transport, addr, metaDSN, adminToken, publicMCP); err != nil {
		log.Fatal(err)
	}

	cat, err := catalog.Load(dataDir)
	if err != nil {
		log.Fatalf("load catalog: %v", err)
	}
	log.Printf("loaded catalog: %d tables, %d relations, %d examples, %d metrics, dialect=%s",
		len(cat.Tables), len(cat.Relations), len(cat.Samples), len(cat.Metrics), cat.Dialect)
	errCount := 0
	for _, issue := range cat.Issues {
		if issue.Level == "error" {
			errCount++
			log.Printf("catalog ERROR [%s] %s %s %s", issue.Source, issue.Message, issue.Table, issue.Column)
		}
	}
	if warn := len(cat.Issues) - errCount; warn > 0 {
		log.Printf("catalog has %d warning(s); call get_catalog_health for details", warn)
	}
	if errCount > 0 {
		log.Printf("catalog compiled with %d validation error(s); affected metadata is ignored or degraded", errCount)
	}

	// meta DB (auth) — optional; missing DSN keeps standalone behavior
	var metaSvc *meta.Service
	if metaDSN != "" {
		store, err := meta.OpenPG(context.Background(), metaDSN)
		if err != nil {
			log.Fatalf("meta db: %v", err)
		}
		metaSvc = meta.NewService(store)
		if user, gen, created, err := metaSvc.Bootstrap(context.Background(), bootstrapAdmin); err != nil {
			log.Fatalf("bootstrap admin: %v", err)
		} else if created {
			if gen != "" {
				log.Printf("=== BOOTSTRAP ADMIN CREATED: username=%q password=%q — 지금 기록하세요; 다시 표시되지 않습니다 ===", user, gen)
			} else {
				log.Printf("bootstrap admin created: %q", user)
			}
		}
		log.Printf("meta db connected: auth ENABLED (login, users, MCP keys, per-user profiles)")
	}

	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "stdio":
		log.Printf("jamypg NL2SQL MCP listening on stdio")
		if err := mcp.ServeStdio(context.Background(), cat, os.Stdin, os.Stdout, mcp.StdioOptions{
			Logf: log.Printf,
		}); err != nil {
			log.Fatal(err)
		}
		return
	case "http", "streamable-http":
	default:
		log.Fatalf("unsupported transport %q: use http or stdio", transport)
	}
	opts := mcp.Options{
		Endpoint:          endpoint,
		AllowedOrigins:    splitCSV(allowOrigins),
		Stateful:          !stateless,
		SSEPost:           ssePost,
		AdminToken:        adminToken,
		FeedbackTenantID:  feedbackTenant,
		OpenMetadataURL:   omURL,
		OpenMetadataToken: omToken,
	}
	srv := mcp.NewServer(cat, opts)
	if metaSvc != nil {
		var oidc *mcp.OIDCProvider
		if oidcIssuer != "" && oidcClientID != "" && oidcSecret != "" && oidcRedirect != "" {
			oidc = &mcp.OIDCProvider{Issuer: oidcIssuer, ClientID: oidcClientID, ClientSecret: oidcSecret, RedirectURL: oidcRedirect}
			log.Printf("Keycloak SSO enabled: issuer=%s client=%s", oidcIssuer, oidcClientID)
		} else if oidcIssuer != "" {
			log.Printf("OIDC partially configured — issuer/client-id/client-secret/redirect-url 4개 모두 필요합니다; SSO 비활성")
		}
		srv.EnableMeta(metaSvc, oidc)
		// stored settings (admin console) override flag/env for runtime-tunable
		// options: master token, allow-origins, OIDC.
		if err := srv.ApplySettings(context.Background()); err != nil {
			log.Printf("apply stored settings: %v", err)
		}
		// datasets: import files→DB on first run, then materialize DB→files
		// and recompile so Postgres is the source of truth.
		if err := srv.InitDatasetStore(context.Background()); err != nil {
			log.Fatalf("init dataset store: %v", err)
		}
		log.Printf("datasets managed in meta DB (jamypg_datasets); edits via /admin persist to Postgres")
		defer metaSvc.Store.Close()
	} else {
		if adminToken == "" {
			log.Printf("admin API auth is DISABLED; set -admin-token or configure -meta-db for full authentication")
		}
		log.Printf("standalone mode (no meta db): login/users/MCP keys disabled; profiles from db_profiles.json")
	}
	log.Printf("admin console: http://%s/admin, API docs: http://%s/docs", addr, addr)
	if syncInterval > 0 {
		srv.StartScheduler(context.Background(), mcp.SchedulerConfig{
			Source: syncSource, Interval: syncInterval, WebhookURL: digestWebhook,
			OpenMetadata: omSync, OpenMetadataScope: omScope, ApplySync: syncApply,
			DBADigest: dbaDigest, DBADigestProfile: dbaDigestProf,
		})
	}
	if err := mcp.ServeServer(addr, srv); err != nil {
		log.Fatal(err)
	}
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
