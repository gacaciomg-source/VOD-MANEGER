package transport

import (
	"encoding/json"
	"testing"
	"time"
)

// O exp_date do Xtream vem em três formatos, e não por descuido de um painel só: cada
// implementação escolheu a sua. Um cliente que aceite apenas uma recusa metade das fontes do
// mercado — e o custo de errar aqui é alto, porque uma data mal lida faz uma fonte boa
// parecer vencida (ou, pior, uma vencida parecer boa).
func TestVencimentoXtreamAceitaOsFormatosReais(t *testing.T) {
	casos := []struct {
		nome    string
		bruto   string
		temData bool
		unix    int64
	}{
		{"texto com número", `"1735689600"`, true, 1735689600},
		{"número puro", `1735689600`, true, 1735689600},
		{"com espaços", `" 1735689600 "`, true, 1735689600},
		// Ausência é o caso legítimo de conta sem prazo. Tratá-la como erro faria fontes
		// boas parecerem vencidas — que é exatamente o defeito oposto ao que isto conserta.
		{"vazio", `""`, false, 0},
		{"nulo", `null`, false, 0},
		{"zero", `"0"`, false, 0},
		{"lixo", `"amanhã"`, false, 0},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			venc, ok := vencimentoXtream(json.RawMessage(c.bruto))
			if ok != c.temData {
				t.Fatalf("%s: havia data? esperava %v, veio %v", c.bruto, c.temData, ok)
			}
			if c.temData && !venc.Equal(time.Unix(c.unix, 0)) {
				t.Fatalf("%s: data errada: %v", c.bruto, venc)
			}
		})
	}
}
