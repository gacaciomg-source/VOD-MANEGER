package roles

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeModule struct {
	name      string
	roles     []Role
	startErr  error
	stopErr   error
	startedAt *[]string
	stoppedAt *[]string
}

func (f *fakeModule) Name() string  { return f.name }
func (f *fakeModule) Roles() []Role { return f.roles }
func (f *fakeModule) Start(context.Context) error {
	if f.startedAt != nil {
		*f.startedAt = append(*f.startedAt, f.name)
	}
	return f.startErr
}
func (f *fakeModule) Stop(context.Context) error {
	if f.stoppedAt != nil {
		*f.stoppedAt = append(*f.stoppedAt, f.name)
	}
	return f.stopErr
}

func TestParseRole(t *testing.T) {
	tests := []struct {
		in      string
		want    Role
		wantErr bool
	}{
		{"manager", RoleManager, false},
		{"NODE", RoleNode, false},
		{"  all  ", RoleAll, false},
		{"", RoleAll, false},
		{"edge", "", true},
	}
	for _, tc := range tests {
		got, err := ParseRole(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseRole(%q): esperava erro", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRole(%q): erro inesperado: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseRole(%q) = %q, esperava %q", tc.in, got, tc.want)
		}
	}
}

func TestRoleCovers(t *testing.T) {
	tests := []struct {
		name     string
		process  Role
		required []Role
		want     bool
	}{
		{"all cobre tudo", RoleAll, []Role{RoleNode}, true},
		{"manager cobre módulo de manager", RoleManager, []Role{RoleManager}, true},
		{"manager não cobre módulo exclusivo de node", RoleManager, []Role{RoleNode}, false},
		{"node cobre módulo compartilhado", RoleNode, []Role{RoleManager, RoleNode}, true},
		{"módulo marcado all roda em qualquer papel", RoleNode, []Role{RoleAll}, true},
		{"lista vazia não cobre", RoleManager, nil, false},
	}
	for _, tc := range tests {
		if got := tc.process.Covers(tc.required); got != tc.want {
			t.Errorf("%s: Covers = %v, esperava %v", tc.name, got, tc.want)
		}
	}
}

func TestRegistryEnabledPorPapel(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, &fakeModule{name: "api", roles: []Role{RoleManager}})
	mustRegister(t, r, &fakeModule{name: "sync", roles: []Role{RoleManager}})
	mustRegister(t, r, &fakeModule{name: "edge", roles: []Role{RoleManager, RoleNode}})
	mustRegister(t, r, &fakeModule{name: "health", roles: []Role{RoleManager, RoleNode}})

	if got, want := r.Names(RoleNode), []string{"edge", "health"}; !reflect.DeepEqual(got, want) {
		t.Errorf("papel node = %v, esperava %v", got, want)
	}
	if got, want := r.Names(RoleManager), []string{"api", "edge", "health", "sync"}; !reflect.DeepEqual(got, want) {
		t.Errorf("papel manager = %v, esperava %v", got, want)
	}
	if got, want := len(r.Enabled(RoleAll)), 4; got != want {
		t.Errorf("papel all = %d módulos, esperava %d", got, want)
	}
}

func TestRegistryRejeitaDuplicadoEInvalido(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, &fakeModule{name: "api", roles: []Role{RoleManager}})

	if err := r.Register(&fakeModule{name: "api", roles: []Role{RoleManager}}); err == nil {
		t.Error("esperava erro em nome duplicado")
	}
	if err := r.Register(&fakeModule{name: "", roles: []Role{RoleManager}}); err == nil {
		t.Error("esperava erro em módulo sem nome")
	}
	if err := r.Register(&fakeModule{name: "x"}); err == nil {
		t.Error("esperava erro em módulo sem papéis")
	}
}

func TestRegistryStartAllFalhaFazRollback(t *testing.T) {
	var started, stopped []string
	r := NewRegistry()
	mustRegister(t, r, &fakeModule{name: "a", roles: []Role{RoleAll}, startedAt: &started, stoppedAt: &stopped})
	mustRegister(t, r, &fakeModule{name: "b", roles: []Role{RoleAll}, startedAt: &started, stoppedAt: &stopped})
	mustRegister(t, r, &fakeModule{
		name: "c", roles: []Role{RoleAll}, startedAt: &started, stoppedAt: &stopped,
		startErr: errors.New("boom"),
	})
	mustRegister(t, r, &fakeModule{name: "d", roles: []Role{RoleAll}, startedAt: &started, stoppedAt: &stopped})

	err := r.StartAll(context.Background(), RoleAll)
	if err == nil {
		t.Fatal("esperava erro do módulo c")
	}
	if got, want := started, []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("iniciados = %v, esperava %v (d não deve iniciar)", got, want)
	}
	if got, want := stopped, []string{"b", "a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("parados = %v, esperava ordem inversa %v", got, want)
	}
}

func TestRegistryStopAllOrdemInversa(t *testing.T) {
	var started, stopped []string
	r := NewRegistry()
	for _, n := range []string{"a", "b", "c"} {
		mustRegister(t, r, &fakeModule{name: n, roles: []Role{RoleAll}, startedAt: &started, stoppedAt: &stopped})
	}
	if err := r.StartAll(context.Background(), RoleAll); err != nil {
		t.Fatalf("StartAll: %v", err)
	}
	if err := r.StopAll(context.Background()); err != nil {
		t.Fatalf("StopAll: %v", err)
	}
	if got, want := stopped, []string{"c", "b", "a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("parados = %v, esperava %v", got, want)
	}
	// StopAll é idempotente: uma segunda chamada não para nada de novo.
	if err := r.StopAll(context.Background()); err != nil {
		t.Fatalf("segundo StopAll: %v", err)
	}
	if len(stopped) != 3 {
		t.Errorf("segundo StopAll parou módulos de novo: %v", stopped)
	}
}

func mustRegister(t *testing.T, r *Registry, m Module) {
	t.Helper()
	if err := r.Register(m); err != nil {
		t.Fatalf("Register(%s): %v", m.Name(), err)
	}
}
