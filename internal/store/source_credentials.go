package store

import (
	"context"
	"time"
)

// SourceCredential é a credencial DE ENTRADA de uma fonte, já cifrada.
//
// O campo SecretEnc nunca deve ser serializado para fora do processo. Não há tag JSON
// nesta struct de propósito: ela não pertence a nenhuma resposta de API.
type SourceCredential struct {
	ID         int64
	SourceID   int64
	Username   string
	SecretEnc  []byte
	KeyVersion int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// SetSourceCredential grava (ou substitui) a credencial de uma fonte.
func (s *Store) SetSourceCredential(ctx context.Context, sourceID int64, username string, secretEnc []byte, keyVersion int) (*SourceCredential, error) {
	var c SourceCredential
	err := s.pool.QueryRow(ctx, `
		INSERT INTO source_credentials (source_id, username, secret_enc, key_version)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (source_id) DO UPDATE
			SET username = EXCLUDED.username,
			    secret_enc = EXCLUDED.secret_enc,
			    key_version = EXCLUDED.key_version,
			    updated_at = now()
		RETURNING id, source_id, username, secret_enc, key_version, created_at, updated_at`,
		sourceID, username, secretEnc, keyVersion,
	).Scan(&c.ID, &c.SourceID, &c.Username, &c.SecretEnc, &c.KeyVersion, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, wrapErr("gravando credencial da fonte", err)
	}
	return &c, nil
}

// GetSourceCredential lê a credencial cifrada de uma fonte.
func (s *Store) GetSourceCredential(ctx context.Context, sourceID int64) (*SourceCredential, error) {
	var c SourceCredential
	err := s.pool.QueryRow(ctx, `
		SELECT id, source_id, username, secret_enc, key_version, created_at, updated_at
		FROM source_credentials WHERE source_id = $1`, sourceID,
	).Scan(&c.ID, &c.SourceID, &c.Username, &c.SecretEnc, &c.KeyVersion, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, wrapErr("lendo credencial da fonte", err)
	}
	return &c, nil
}

// DeleteSourceCredential remove a credencial de uma fonte.
func (s *Store) DeleteSourceCredential(ctx context.Context, sourceID int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM source_credentials WHERE source_id = $1`, sourceID)
	if err != nil {
		return wrapErr("removendo credencial da fonte", err)
	}
	if tag.RowsAffected() == 0 {
		return wrapErr("removendo credencial da fonte", ErrNotFound)
	}
	return nil
}
