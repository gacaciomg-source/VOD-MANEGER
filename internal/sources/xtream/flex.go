package xtream

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Painéis compatíveis com Xtream são inconsistentes quanto aos tipos: o mesmo campo vem
// como número numa instalação e como string em outra, e às vezes como null ou "".
//
// Os tipos abaixo aceitam as duas formas. É o que impede que uma fonte derrube a
// sincronização inteira por causa de um campo tipado de forma diferente (risco R8 da
// doc 04). Nenhum deles inventa valor: entrada inválida vira "ausente", não zero.

// flexString aceita string ou número.
type flexString struct {
	Value string
	Set   bool
}

func (f *flexString) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == `""` || s == "" {
		return nil
	}
	if s[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return nil // tolerante por desenho: campo estranho não derruba a run
		}
		f.Value, f.Set = strings.TrimSpace(v), strings.TrimSpace(v) != ""
		return nil
	}
	if s[0] == '{' || s[0] == '[' {
		// Objeto ou lista onde deveria haver um escalar: o campo é ignorado, não
		// convertido em texto. Serializar a estrutura crua produziria um id inventado.
		return nil
	}
	f.Value, f.Set = s, true
	return nil
}

func (f flexString) String() string { return f.Value }

// flexInt aceita número, string numérica, ou ausência.
type flexInt struct {
	Value int
	Set   bool
}

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		f.Value, f.Set = n, true
		return nil
	}
	// Alguns painéis mandam "1.0" onde deveria vir 1.
	if fl, err := strconv.ParseFloat(s, 64); err == nil {
		f.Value, f.Set = int(fl), true
	}
	return nil
}

// Ptr devolve o valor como ponteiro, ou nil se ausente.
func (f flexInt) Ptr() *int {
	if !f.Set {
		return nil
	}
	v := f.Value
	return &v
}

// flexFloat aceita número ou string numérica.
type flexFloat struct {
	Value float64
	Set   bool
}

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	if fl, err := strconv.ParseFloat(s, 64); err == nil {
		f.Value, f.Set = fl, true
	}
	return nil
}

// Ptr devolve o valor como ponteiro, ou nil se ausente.
func (f flexFloat) Ptr() *float64 {
	if !f.Set {
		return nil
	}
	v := f.Value
	return &v
}

// anoDeData extrai o ano de formatos comuns de data ("2014-11-07", "2014", "07/11/2014").
//
// Devolve 0 quando não consegue — nunca chuta.
func anoDeData(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// ISO e variantes: o ano são os 4 primeiros dígitos.
	if len(s) >= 4 {
		if n, err := strconv.Atoi(s[:4]); err == nil && n >= 1888 && n <= 2200 {
			return n
		}
	}
	// dd/mm/yyyy.
	if partes := strings.Split(s, "/"); len(partes) == 3 {
		if n, err := strconv.Atoi(strings.TrimSpace(partes[2])); err == nil && n >= 1888 && n <= 2200 {
			return n
		}
	}
	return 0
}

// segundosDeMinutos converte "45" (minutos) em segundos. Devolve nil quando inválido.
func segundosDeMinutos(f flexInt) *int {
	if !f.Set || f.Value <= 0 {
		return nil
	}
	v := f.Value * 60
	return &v
}
