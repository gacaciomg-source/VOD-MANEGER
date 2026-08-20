// Package roles implementa o gate de papéis (Manager / Node) e o registro de módulos.
//
// Esta é a costura que permite, no futuro, separar Manager e Nodes sem reescrever o
// núcleo: cada módulo declara em quais papéis roda e o processo instancia apenas os
// módulos do papel corrente. A Fase 1 NÃO implementa multi-node — apenas garante que
// a separação depois não exija reescrita.
package roles

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Role é o papel de um processo.
type Role string

const (
	// RoleManager roda o plano de controle: API, painel, sync, lifecycle.
	RoleManager Role = "manager"
	// RoleNode roda apenas o plano de dados: edge (streaming) e health.
	RoleNode Role = "node"
	// RoleAll roda tudo no mesmo processo. Padrão da v1.
	RoleAll Role = "all"
)

// ParseRole valida e normaliza um papel vindo de configuração.
func ParseRole(s string) (Role, error) {
	switch Role(strings.ToLower(strings.TrimSpace(s))) {
	case RoleManager:
		return RoleManager, nil
	case RoleNode:
		return RoleNode, nil
	case RoleAll, "":
		return RoleAll, nil
	default:
		return "", fmt.Errorf("papel inválido %q (use manager, node ou all)", s)
	}
}

// Covers informa se o papel do processo cobre um dos papéis exigidos por um módulo.
func (r Role) Covers(required []Role) bool {
	if r == RoleAll {
		return true
	}
	for _, want := range required {
		if want == r || want == RoleAll {
			return true
		}
	}
	return false
}

// Module é uma unidade de funcionalidade com ciclo de vida próprio.
//
// Start deve retornar rápido: trabalho contínuo vai para goroutines internas do módulo.
// Stop deve ser idempotente e respeitar o context de shutdown.
type Module interface {
	Name() string
	Roles() []Role
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Registry guarda os módulos e decide quais sobem para o papel corrente.
type Registry struct {
	modules []Module
	started []Module
}

// NewRegistry cria um registro vazio.
func NewRegistry() *Registry { return &Registry{} }

// Register adiciona um módulo. Nomes duplicados são erro de programação.
func (r *Registry) Register(m Module) error {
	if m == nil {
		return fmt.Errorf("módulo nulo")
	}
	if m.Name() == "" {
		return fmt.Errorf("módulo sem nome")
	}
	if len(m.Roles()) == 0 {
		return fmt.Errorf("módulo %q não declarou papéis", m.Name())
	}
	for _, existing := range r.modules {
		if existing.Name() == m.Name() {
			return fmt.Errorf("módulo %q registrado duas vezes", m.Name())
		}
	}
	r.modules = append(r.modules, m)
	return nil
}

// Enabled devolve, em ordem de registro, os módulos que rodam no papel informado.
func (r *Registry) Enabled(role Role) []Module {
	out := make([]Module, 0, len(r.modules))
	for _, m := range r.modules {
		if role.Covers(m.Roles()) {
			out = append(out, m)
		}
	}
	return out
}

// Names devolve os nomes habilitados, ordenados, para log e diagnóstico.
func (r *Registry) Names(role Role) []string {
	mods := r.Enabled(role)
	names := make([]string, 0, len(mods))
	for _, m := range mods {
		names = append(names, m.Name())
	}
	sort.Strings(names)
	return names
}

// StartAll sobe os módulos do papel na ordem de registro. Se um falhar, os já iniciados
// são parados na ordem inversa antes de retornar o erro.
func (r *Registry) StartAll(ctx context.Context, role Role) error {
	for _, m := range r.Enabled(role) {
		if err := m.Start(ctx); err != nil {
			stopCtx := context.WithoutCancel(ctx)
			_ = r.StopAll(stopCtx)
			return fmt.Errorf("iniciando módulo %q: %w", m.Name(), err)
		}
		r.started = append(r.started, m)
	}
	return nil
}

// StopAll para os módulos iniciados na ordem inversa, agregando os erros.
func (r *Registry) StopAll(ctx context.Context) error {
	var errs []error
	for i := len(r.started) - 1; i >= 0; i-- {
		if err := r.started[i].Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("parando módulo %q: %w", r.started[i].Name(), err))
		}
	}
	r.started = nil
	if len(errs) == 0 {
		return nil
	}
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}
