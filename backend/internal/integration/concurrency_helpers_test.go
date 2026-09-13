package integration

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"hazop-safeguard-coverage/backend/internal/model"
	"hazop-safeguard-coverage/backend/internal/repository"
)

// testRoleHeader tags a request so concurrency tests can identify the intended
// winner/loser. It is copied into the request context and read by the
// instrumented repository.
const testRoleHeader = "X-Test-Role"

type testRoleKey struct{}

func roleFromContext(ctx context.Context) string {
	role, _ := ctx.Value(testRoleKey{}).(string)
	return role
}

// testRoleMiddleware copies the X-Test-Role header into the outbound request
// context so that the value reaches the service/repository via
// c.Request.Context().
func testRoleMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if role := c.GetHeader(testRoleHeader); role != "" {
			ctx := context.WithValue(c.Request.Context(), testRoleKey{}, role)
			c.Request = c.Request.WithContext(ctx)
		}
		c.Next()
	}
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique") || strings.Contains(message, "duplicate")
}

// instrumentedTx wraps the in-transaction repository to surface database-level
// unique-index violations on the binding INSERT.
type instrumentedTx struct {
	repository.RectificationItemRepository
	onUnique func()
}

func (t *instrumentedTx) CreateBinding(ctx context.Context, binding *model.RectificationBinding) error {
	err := t.RectificationItemRepository.CreateBinding(ctx, binding)
	if isUniqueViolation(err) && t.onUnique != nil {
		t.onUnique()
	}
	return err
}

// staggeredBindingRepo wraps the rectification repository so that a request
// tagged as the "loser" is paused inside its completion transaction (after its
// pre-check FindActiveBindings has passed and BEGIN has been issued, but before
// the binding INSERT) until the test releases it. This guarantees the loser's
// duplicate INSERT happens strictly after the winner committed, so the only
// thing that can reject it is the safeguard unique index.
type staggeredBindingRepo struct {
	repository.RectificationItemRepository
	entered          chan<- struct{}
	release          <-chan struct{}
	uniqueViolations chan<- struct{}
}

func newStaggeredRepo(
	db *gorm.DB,
	entered chan<- struct{},
	release <-chan struct{},
	uniqueViolations chan<- struct{},
) repository.RectificationItemRepository {
	return &staggeredBindingRepo{
		RectificationItemRepository: repository.NewRectificationItemRepository(db),
		entered:                     entered,
		release:                     release,
		uniqueViolations:            uniqueViolations,
	}
}

func (r *staggeredBindingRepo) WithTx(
	ctx context.Context,
	fn func(repository.RectificationItemRepository) error,
) error {
	return r.RectificationItemRepository.WithTx(ctx, func(tx repository.RectificationItemRepository) error {
		instrumented := &instrumentedTx{
			RectificationItemRepository: tx,
			onUnique: func() {
				select {
				case r.uniqueViolations <- struct{}{}:
				default:
				}
			},
		}
		if roleFromContext(ctx) == "loser" {
			// Pre-check has already run; the transaction is open with no write
			// lock held. Signal arrival and wait for the winner to commit.
			close(r.entered)
			<-r.release
		}
		return fn(instrumented)
	})
}
