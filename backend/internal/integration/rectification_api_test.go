// Package integration contains interface-level tests that drive the real HTTP
// router together with authentication, RBAC, request-ID, audit and error
// handling middleware. They use an isolated in-memory SQLite database per test
// and obtain bearer tokens through the real /auth/login endpoint, so they can
// be repeated without any external service.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"hazop-safeguard-coverage/backend/internal/algorithm"
	"hazop-safeguard-coverage/backend/internal/config"
	"hazop-safeguard-coverage/backend/internal/constants"
	"hazop-safeguard-coverage/backend/internal/dto"
	"hazop-safeguard-coverage/backend/internal/handler"
	"hazop-safeguard-coverage/backend/internal/middleware"
	"hazop-safeguard-coverage/backend/internal/model"
	"hazop-safeguard-coverage/backend/internal/repository"
	"hazop-safeguard-coverage/backend/internal/router"
	"hazop-safeguard-coverage/backend/internal/service"
	"hazop-safeguard-coverage/backend/internal/util"
)

const (
	testPassword = "password123"
	baseURL      = "/api/v1"
)

// apiFixture holds a fully wired application identical in shape to cmd/server
// (same repositories, services, handlers, middleware and route table) backed
// by a private in-memory database.
type apiFixture struct {
	t           *testing.T
	server      *httptest.Server
	db          *gorm.DB
	cfg         config.Config
	tokens      map[string]string
	users       map[string]model.User
	nodes       repository.ProcessNodeRepository
	scenarios   repository.DeviationScenarioRepository
	safeguards  repository.SafeguardRepository
	evaluations repository.CoverageEvaluationRepository
	items       repository.RectificationItemRepository
	audits      repository.AuditRepository
}

type envelope struct {
	Code      string          `json:"code"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	RequestID string          `json:"request_id"`
}

type apiResponse struct {
	StatusCode int
	Envelope   envelope
}

type apiClient struct {
	fixture *apiFixture
	token   string
	headers map[string]string
}

func newAPIFixture(t *testing.T, opts ...fixtureOption) *apiFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)

	options := fixtureOptions{maxConns: 1}
	for _, opt := range opts {
		opt(&options)
	}

	// Default: a shared-cache in-memory database on one connection, mirroring the
	// production SQLite pool and serializing writes. Concurrency tests opt into a
	// multi-connection, WAL-backed file database so requests really overlap.
	var dsn string
	if options.maxConns > 1 {
		dsn = fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=10000",
			filepath.Join(t.TempDir(), "app.db"))
	} else {
		dsn = fmt.Sprintf("file:%s?mode=memory&cache=shared", sanitizeDSN(t.Name()))
	}
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.ProcessNode{}, &model.DeviationScenario{},
		&model.Safeguard{}, &model.CoverageEvaluation{}, &model.RectificationItem{},
		&model.RectificationBinding{}, &model.AuditLog{},
	); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("raw db: %v", err)
	}
	sqlDB.SetMaxOpenConns(options.maxConns)
	sqlDB.SetMaxIdleConns(options.maxConns)
	sqlDB.SetConnMaxLifetime(time.Hour)
	t.Cleanup(func() { _ = sqlDB.Close() })

	cfg := config.Config{
		Port: "0", DBDriver: "sqlite", DBDSN: dsn,
		JWTSecret: "integration-test-secret", JWTIssuer: "hazop-safeguard-coverage",
		JWTExpiry: time.Hour, LoginLimitPerMinute: 10000, RunLimitPerMinute: 10000,
	}

	nodeRepo := repository.NewProcessNodeRepository(db)
	scenarioRepo := repository.NewDeviationScenarioRepository(db)
	safeguardRepo := repository.NewSafeguardRepository(db)
	evaluationRepo := repository.NewCoverageEvaluationRepository(db)
	itemRepo := repository.NewRectificationItemRepository(db)
	auditRepo := repository.NewAuditRepository(db)
	userRepo := repository.NewUserRepository(db)

	// The service may be handed an instrumented item repository (concurrency
	// tests); test-side assertions keep using the plain repository.
	serviceItemRepo := repository.RectificationItemRepository(itemRepo)
	if options.itemRepo != nil {
		serviceItemRepo = options.itemRepo(db)
	}

	rectificationHandler := handler.NewRectificationItemHandler(service.NewRectificationItemService(
		serviceItemRepo, evaluationRepo, safeguardRepo, auditRepo,
	))

	auth := middleware.NewAuthenticator(userRepo, cfg)
	loginLimiter := middleware.NewRateLimiter(cfg.LoginLimitPerMinute)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	engine := gin.New()
	engine.Use(middleware.RequestID(), middleware.Recovery(logger), middleware.ErrorHandler(logger))
	v1 := engine.Group("/api/v1")
	v1.POST("/auth/login", loginLimiter.Middleware("login"), auth.Login)
	api := v1.Group("")
	authenticated := []gin.HandlerFunc{auth.RequireAuth()}
	if options.extraMiddleware != nil {
		authenticated = append(authenticated, options.extraMiddleware)
	}
	authenticated = append(authenticated, middleware.Audit(auditRepo))
	api.Use(authenticated...)
	router.RegisterRectificationItemRoutes(api, rectificationHandler)
	engine.NoRoute(func(c *gin.Context) {
		util.Fail(c, util.NewError(http.StatusNotFound, util.CodeNotFound, "route was not found"))
	})

	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)

	fixture := &apiFixture{
		t: t, server: server, db: db, cfg: cfg,
		tokens:      map[string]string{},
		users:       map[string]model.User{},
		nodes:       nodeRepo,
		scenarios:   scenarioRepo,
		safeguards:  safeguardRepo,
		evaluations: evaluationRepo,
		items:       itemRepo,
		audits:      auditRepo,
	}
	fixture.seedUsers()
	return fixture
}

// fixtureOptions tunes the private application harness.
type fixtureOptions struct {
	// maxConns sets the database connection-pool size. Values above 1 switch the
	// harness to a WAL-backed file database so concurrent writes genuinely race.
	maxConns int
	// itemRepo optionally replaces the rectification repository used by the
	// service (e.g. to instrument transaction boundaries in concurrency tests).
	itemRepo func(*gorm.DB) repository.RectificationItemRepository
	// extraMiddleware is installed inside the authenticated group, so it runs
	// after RequireAuth and can read both request headers and the actor.
	extraMiddleware gin.HandlerFunc
}

// fixtureOption customizes an apiFixture.
type fixtureOption func(*fixtureOptions)

func withPoolSize(size int) fixtureOption {
	return func(o *fixtureOptions) { o.maxConns = size }
}

func withItemRepoOverride(fn func(*gorm.DB) repository.RectificationItemRepository) fixtureOption {
	return func(o *fixtureOptions) { o.itemRepo = fn }
}

func withExtraMiddleware(mw gin.HandlerFunc) fixtureOption {
	return func(o *fixtureOptions) { o.extraMiddleware = mw }
}

func sanitizeDSN(name string) string {
	var b bytes.Buffer
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "integration"
	}
	return b.String()
}

func (f *apiFixture) seedUsers() {
	accounts := []struct {
		username, display, role string
	}{
		{"admin", "Administrator", string(constants.RoleAdmin)},
		{"engineer", "Process Engineer", string(constants.RoleProcessEngineer)},
		{"reviewer", "Safety Reviewer", string(constants.RoleSafetyReviewer)},
		{"auditor", "Compliance Auditor", string(constants.RoleAuditor)},
	}
	now := time.Now().UTC()
	for _, account := range accounts {
		hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
		if err != nil {
			f.t.Fatalf("hash password for %s: %v", account.username, err)
		}
		user := model.User{
			Username: account.username, DisplayName: account.display,
			PasswordHash: string(hash), Role: account.role, Active: true,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := f.db.Create(&user).Error; err != nil {
			f.t.Fatalf("create user %s: %v", account.username, err)
		}
		f.users[account.username] = user
	}
}

// login performs a real POST /api/v1/auth/login and caches the bearer token.
func (f *apiFixture) login(username string) string {
	if token, ok := f.tokens[username]; ok {
		return token
	}
	resp := f.request(http.MethodPost, baseURL+"/auth/login", "", map[string]any{
		"username": username, "password": testPassword,
	})
	if resp.StatusCode != http.StatusOK {
		f.t.Fatalf("login as %s failed: status=%d body=%s", username, resp.StatusCode, resp.Envelope.Message)
	}
	var data struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(resp.Envelope.Data, &data); err != nil {
		f.t.Fatalf("decode login response: %v", err)
	}
	if data.Token == "" {
		f.t.Fatalf("login returned empty token")
	}
	f.tokens[username] = data.Token
	return data.Token
}

// clientFor returns an HTTP client already authenticated as the given user.
func (f *apiFixture) clientFor(username string) *apiClient {
	return &apiClient{fixture: f, token: f.login(username)}
}

// anonClient returns an HTTP client that never sends an Authorization header.
func (f *apiFixture) anonClient() *apiClient { return &apiClient{fixture: f} }

// forgedToken mints a structurally valid JWT signed with the wrong secret so
// the request reaches (and must be rejected by) RequireAuth.
func (f *apiFixture) forgedToken() string {
	claims := middleware.Claims{
		UserID: 999, Username: "forged", DisplayName: "Forged", Role: "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    f.cfg.JWTIssuer,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("a-completely-wrong-secret"))
	if err != nil {
		f.t.Fatalf("sign forged token: %v", err)
	}
	return token
}

func (f *apiFixture) request(method, path, token string, body any) apiResponse {
	return f.requestWith(method, path, token, body, nil)
}

func (f *apiFixture) requestWith(method, path, token string, body any, headers map[string]string) apiResponse {
	f.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			f.t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.server.URL+path, reader)
	if err != nil {
		f.t.Fatalf("new request %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("do request %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatalf("read response body: %v", err)
	}
	parsed := apiResponse{StatusCode: resp.StatusCode}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &parsed.Envelope); err != nil {
			f.t.Fatalf("decode response envelope (%d): %v: %s", resp.StatusCode, err, string(raw))
		}
	}
	return parsed
}

func (c *apiClient) do(method, path string, body any) apiResponse {
	return c.fixture.requestWith(method, path, c.token, body, c.headers)
}

// withHeader returns a derived client that attaches an extra header to every
// request; concurrency tests use it to carry a winner/loser marker that the
// instrumented repository can read from the request context.
func (c *apiClient) withHeader(key, value string) *apiClient {
	headers := make(map[string]string, len(c.headers)+1)
	for k, v := range c.headers {
		headers[k] = v
	}
	headers[key] = value
	return &apiClient{fixture: c.fixture, token: c.token, headers: headers}
}

func (r apiResponse) decodeData(t *testing.T, target any) {
	t.Helper()
	if err := json.Unmarshal(r.Envelope.Data, target); err != nil {
		t.Fatalf("decode data payload: %v: %s", err, string(r.Envelope.Data))
	}
}

func (r apiResponse) expect(t *testing.T, status int, code util.ErrorCode) {
	t.Helper()
	if r.StatusCode != status || r.Envelope.Code != string(code) {
		t.Fatalf("expected %d %s, got %d %s (%s)", status, code, r.StatusCode, r.Envelope.Code, r.Envelope.Message)
	}
}

// ---- domain fixtures -------------------------------------------------------

func (f *apiFixture) createNodeScenario(t *testing.T, key string) (model.ProcessNode, model.DeviationScenario) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	node := model.ProcessNode{
		NodeCode: "R-" + key, Name: "Node " + key, UnitName: "Test Unit", Medium: "solvent",
		DesignPressure: 1.5, DesignTemperature: 120, OwnerTeam: "test", Status: "active",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := f.nodes.Create(ctx, &node); err != nil {
		t.Fatalf("create node: %v", err)
	}
	scenario := model.DeviationScenario{
		ProcessNodeID: node.ID, Guideword: "more", Parameter: "pressure-" + key,
		Cause: "blocked outlet " + key, Consequence: "vessel overpressure " + key,
		Likelihood: 3, Severity: 5, ScenarioState: "analyzed", Version: 1,
		CreatedBy: f.users["engineer"].ID, CreatedByName: "engineer",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := f.scenarios.Create(ctx, &scenario); err != nil {
		t.Fatalf("create scenario: %v", err)
	}
	return node, scenario
}

// createCompletedEvaluation stores a completed evaluation carrying one
// uncovered path that can later generate a rectification item.
func (f *apiFixture) createCompletedEvaluation(t *testing.T, key string, scenarioID uint, pathID, cause, consequence string) model.CoverageEvaluation {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uncovered := fmt.Sprintf(
		`[{"path_id":%q,"node_code":"R-%s","cause":%q,"consequence":%q,"safeguard_ids":[],"independence_keys":[],"combined_protection":0,"covered":false,"reason":"no effective safeguard"}]`,
		pathID, key, cause, consequence,
	)
	evaluation := model.CoverageEvaluation{
		ScenarioID: scenarioID, AlgorithmVersion: algorithm.Version,
		InputSnapshot: "{}", InputHash: util.HashString("eval-" + key),
		CoverageScore: 0, UncoveredPaths: uncovered, DeduplicatedSafeguards: "[]",
		RiskRankBefore: "high", RiskRankAfter: "high",
		EvaluationState: "completed", Explanation: "{}",
		EvaluatedBy: f.users["engineer"].ID, EvaluatedByName: "engineer", EvaluatedAt: now,
		IdempotencyKey: "idem-" + key, CreatedAt: now, UpdatedAt: now,
	}
	if err := f.evaluations.Create(ctx, &evaluation); err != nil {
		t.Fatalf("create evaluation: %v", err)
	}
	return evaluation
}

func (f *apiFixture) addSafeguard(t *testing.T, name string, scenarioID uint, createdAt time.Time) model.Safeguard {
	t.Helper()
	ctx := context.Background()
	verified := time.Now().UTC().Add(-24 * time.Hour)
	safeguard := model.Safeguard{
		Name: name, SafeguardType: "interlock", TargetScenarioID: scenarioID,
		IndependenceKey: "KEY-" + name, Effectiveness: 0.8, TestIntervalDays: 365,
		LastVerifiedAt: &verified, LifecycleState: "active", EvidenceNote: "integration evidence",
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := f.safeguards.Create(ctx, &safeguard); err != nil {
		t.Fatalf("create safeguard %s: %v", name, err)
	}
	return safeguard
}

// generateItem drives the real generate endpoint and returns the new item.
func (f *apiFixture) generateItem(t *testing.T, client *apiClient, evaluationID uint, owner string) dto.RectificationItemResponse {
	t.Helper()
	resp := client.do(http.MethodPost, baseURL+"/rectification-items/generate", map[string]any{
		"evaluation_id": evaluationID, "owner_name": owner,
	})
	resp.expect(t, http.StatusCreated, "OK")
	var result dto.GenerateRectificationResponse
	resp.decodeData(t, &result)
	if len(result.Created) != 1 || len(result.Skipped) != 0 {
		t.Fatalf("expected exactly 1 created item, got %d created %d skipped", len(result.Created), len(result.Skipped))
	}
	return result.Created[0]
}

func (f *apiFixture) transition(t *testing.T, client *apiClient, id uint, toState, reason string) apiResponse {
	t.Helper()
	body := map[string]any{"to_state": toState}
	if reason != "" {
		body["reason"] = reason
	}
	return client.do(http.MethodPost, fmt.Sprintf("%s/rectification-items/%d/transition", baseURL, id), body)
}

func (f *apiFixture) getItem(t *testing.T, id uint) model.RectificationItem {
	t.Helper()
	item, err := f.items.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("load item %d: %v", id, err)
	}
	return item
}

func (f *apiFixture) bindingCount(t *testing.T, safeguardID uint) int {
	t.Helper()
	bindings, err := f.items.FindActiveBindings(context.Background(), []uint{safeguardID})
	if err != nil {
		t.Fatalf("find bindings: %v", err)
	}
	return len(bindings)
}

// assertItemUntouched verifies a failed request left both the rectification
// item and its binding set byte-for-byte in their prior state.
func (f *apiFixture) assertItemUntouched(t *testing.T, id uint, before model.RectificationItem) {
	t.Helper()
	after := f.getItem(t, id)
	if after.State != before.State {
		t.Fatalf("item %d state changed %q -> %q after a failed request", id, before.State, after.State)
	}
	if !uintPtrEqual(after.CompletedBy, before.CompletedBy) || !timePtrEqual(after.CompletedAt, before.CompletedAt) {
		t.Fatalf("item %d completion fields changed after a failed request", id)
	}
	if after.VoidReason != before.VoidReason || after.EvidenceNote != before.EvidenceNote {
		t.Fatalf("item %d text fields changed after a failed request", id)
	}
	if len(after.Bindings) != len(before.Bindings) {
		t.Fatalf("item %d binding count changed %d -> %d after a failed request",
			id, len(before.Bindings), len(after.Bindings))
	}
}

func uintPtrEqual(a, b *uint) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func timePtrEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
