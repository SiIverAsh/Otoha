package handlers

import (
	"context"

	"github.com/SiIverAsh/otoha/internal/db"
)

type ctxUserKey struct{}

func WithUser(ctx context.Context, u *db.User) context.Context {
	return context.WithValue(ctx, ctxUserKey{}, u)
}

func UserFromContext(ctx context.Context) *db.User {
	if u, ok := ctx.Value(ctxUserKey{}).(*db.User); ok && u != nil {
		return u
	}
	return &db.User{ID: 1, Username: "default", IsAdmin: true}
}
