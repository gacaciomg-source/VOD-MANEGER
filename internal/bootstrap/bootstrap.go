// Package bootstrap cria o primeiro administrador quando o banco está vazio.
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"vodmanager/internal/auth"
	"vodmanager/internal/store"
)

// Result descreve o que o bootstrap fez.
type Result struct {
	Created bool
	// GeneratedPassword só é preenchida quando o sistema gerou a senha. Ela é exibida
	// UMA única vez, no log de partida, e nunca é recuperável depois.
	GeneratedPassword string
	Username          string
}

// EnsureAdmin cria o usuário administrador inicial se ainda não houver nenhum usuário.
//
// É idempotente: com qualquer usuário existente, não faz nada. Nunca reseta senha de
// instalação existente — isso seria uma porta dos fundos permanente via variável de
// ambiente.
func EnsureAdmin(ctx context.Context, st *store.Store, log *slog.Logger, username, password string) (Result, error) {
	count, err := st.CountUsers(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("bootstrap: contando usuários: %w", err)
	}
	if count > 0 {
		return Result{}, nil
	}
	if username == "" {
		username = "admin"
	}

	generated := ""
	if password == "" {
		password, err = auth.GeneratePassword()
		if err != nil {
			return Result{}, fmt.Errorf("bootstrap: gerando senha: %w", err)
		}
		generated = password
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return Result{}, fmt.Errorf("bootstrap: preparando senha: %w", err)
	}
	user, err := st.CreateUser(ctx, username, hash, store.RoleAdmin)
	if err != nil {
		return Result{}, fmt.Errorf("bootstrap: criando administrador: %w", err)
	}

	log.Info("administrador inicial criado", "username", user.Username, "user_id", user.ID)
	return Result{Created: true, GeneratedPassword: generated, Username: user.Username}, nil
}
