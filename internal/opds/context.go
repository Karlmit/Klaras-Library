package opds

import (
	"context"

	"github.com/Karlmit/Klaras-Library/internal/auth"
)

type ctxKey int

const userKey ctxKey = iota

func withUser(ctx context.Context, u *auth.User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// UserFrom returns the authenticated OPDS client, if any.
func UserFrom(ctx context.Context) *auth.User {
	u, _ := ctx.Value(userKey).(*auth.User)
	return u
}
