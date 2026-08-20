package sync

import (
	"testing"

	"vodmanager/internal/ingest"
)

// O schema aceita só três valores. Dois casos produziam outros — e a recusa era engolida,
// então a decisão simplesmente não era gravada e ninguém percebia.
func TestDecisaoParaBancoSempreProduzValorAceito(t *testing.T) {
	aceitos := map[string]bool{"grouped": true, "pending_review": true, "rejected": true}

	casos := []ingest.MatchResult{
		{Decision: ingest.DecisionGrouped, Score: 95},
		{Decision: ingest.DecisionPendingReview, Score: 40},
		{Decision: ingest.DecisionRejected, Score: 10},
		{Decision: ingest.DecisionLocked, Score: 100},
		{}, // episódio: alvo vem da estrutura, sem score
		{Decision: ingest.MatchDecision("inventado")},
	}
	for _, c := range casos {
		d := decisaoParaBanco(c)
		if !aceitos[d.Decision] {
			t.Errorf("decisão %q virou %q, que o schema recusa", c.Decision, d.Decision)
		}
	}
}

// A trava não pode se perder na tradução: ela é o que impede o algoritmo de desfazer um
// agrupamento decidido por uma pessoa.
func TestDecisaoTravadaPreservaATrava(t *testing.T) {
	d := decisaoParaBanco(ingest.MatchResult{Decision: ingest.DecisionLocked, Score: 100})
	if d.Decision != "grouped" {
		t.Errorf("decisão = %q, esperava grouped", d.Decision)
	}
	if !d.Locked {
		t.Error("a trava se perdeu na tradução")
	}
}

// Episódio agrupado pela estrutura da série é agrupamento de verdade, e com certeza —
// não é um palpite de similaridade com confiança zero.
func TestEpisodioSemScoreVaiComoAgrupadoComCerteza(t *testing.T) {
	d := decisaoParaBanco(ingest.MatchResult{})
	if d.Decision != "grouped" {
		t.Errorf("decisão = %q", d.Decision)
	}
	if d.Confidence != 100 {
		t.Errorf("confiança = %d, esperava 100", d.Confidence)
	}
	if d.Note == "" {
		t.Error("a origem do agrupamento deveria ficar registrada na nota")
	}
	if d.Locked {
		t.Error("agrupamento automático não pode nascer travado")
	}
}

// Os valores que o schema já aceita passam intactos.
func TestDecisaoValidaNaoEhAlterada(t *testing.T) {
	r := ingest.MatchResult{Decision: ingest.DecisionPendingReview, Score: 42}
	d := decisaoParaBanco(r)
	if d.Decision != "pending_review" || d.Confidence != 42 {
		t.Errorf("decisão válida foi alterada: %+v", d)
	}
	if d.Note != "" {
		t.Errorf("nota inesperada: %q", d.Note)
	}
}
