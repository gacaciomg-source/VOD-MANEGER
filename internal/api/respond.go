// Package api expõe a REST administrativa da Fase 1.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"vodmanager/internal/store"
)

// maxBodyBytes limita o corpo das requisições administrativas. Payload maior que isso
// nesta API é sempre erro ou abuso.
const maxBodyBytes = 1 << 20 // 1 MiB

// errorBody é o formato único de erro da API.
type errorBody struct {
	Error struct {
		Code    string   `json:"code"`
		Message string   `json:"message"`
		Fields  []string `json:"fields,omitempty"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, log *slog.Logger, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// Cabeçalho já foi enviado: só resta registrar.
		log.Error("falha ao escrever resposta JSON", "erro", err)
	}
}

func writeError(w http.ResponseWriter, log *slog.Logger, status int, code, message string, fields ...string) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = message
	body.Error.Fields = fields
	writeJSON(w, log, status, body)
}

// decodeJSON lê o corpo com limite de tamanho e rejeita campos desconhecidos.
//
// Rejeitar campo desconhecido é deliberado: um erro de digitação em `max_connections`
// falharia em silêncio e o limite de conexões da fonte não seria aplicado.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("o corpo deve conter um único objeto JSON")
	}
	return nil
}

func pathID(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, name), 10, 64)
}

// storeStatus traduz erros da camada store em status HTTP.
func storeStatus(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, store.ErrConflict):
		return http.StatusConflict, "conflict"
	case errors.Is(err, store.ErrInvalid):
		return http.StatusBadRequest, "invalid"
	default:
		return http.StatusInternalServerError, "internal"
	}
}
