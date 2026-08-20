package ingest

import (
	"strings"
	"testing"
)

func TestFieldCatalogEConsistente(t *testing.T) {
	catalogo := FieldCatalog()
	if len(catalogo) == 0 {
		t.Fatal("catálogo de campos vazio")
	}

	vistos := make(map[string]bool, len(catalogo))
	for _, f := range catalogo {
		if f.Path == "" {
			t.Error("campo sem caminho")
		}
		if vistos[f.Path] {
			t.Errorf("campo %q listado duas vezes", f.Path)
		}
		vistos[f.Path] = true

		switch f.Level {
		case LevelGuaranteed, LevelOptional, LevelVendor:
		default:
			t.Errorf("campo %q tem nível inválido %q", f.Path, f.Level)
		}
	}
}

// Enquanto as amostras reais não chegarem, nada de nível Vendor pode ter virado
// dependência de lógica. Este teste registra o estado esperado: apenas o raw_payload.
func TestApenasRawPayloadEVendor(t *testing.T) {
	vendor := VendorFields()
	if len(vendor) != 1 {
		t.Fatalf("campos Vendor = %d, esperava exatamente 1 (raw_payload)", len(vendor))
	}
	if !strings.HasPrefix(vendor[0].Path, "raw_payload") {
		t.Errorf("campo Vendor inesperado: %q", vendor[0].Path)
	}
}

// Os campos que o contrato promete como Garantidos precisam estar cobertos por um teste
// que verifique o preenchimento. Esta lista é o vínculo entre docs/07 §2 e o código.
func TestCamposGarantidosDeclaradosNoCatalogo(t *testing.T) {
	obrigatorios := []string{
		"kind",
		"kind_provenance.source",
		"kind_provenance.rule",
		"variant.source_id",
		"variant.provenance",
		"category.declared_name",
		"category.normalized_name",
		"category.content_type",
		"category.provenance",
		"media.container_provenance",
		"signals.quality_tags",
		"signals.language_tags",
		"signals.tags_provenance",
		"digest",
	}

	catalogo := FieldCatalog()
	nivel := make(map[string]FieldLevel, len(catalogo))
	for _, f := range catalogo {
		nivel[f.Path] = f.Level
	}

	for _, path := range obrigatorios {
		lvl, ok := nivel[path]
		if !ok {
			t.Errorf("campo %q não está no catálogo", path)
			continue
		}
		if lvl != LevelGuaranteed {
			t.Errorf("campo %q está como %q, deveria ser Garantido", path, lvl)
		}
	}
}
