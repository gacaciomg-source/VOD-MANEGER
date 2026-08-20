# VOD Manager — 09. Modo de cache por fonte e por conteúdo

> Decisão de projeto levantada pelo administrador em 2026-08-19, durante a validação do
> proxy de streaming. **Implementação prevista para a Fase 5**, junto com o cache — antes
> disso, a configuração não teria efeito e só confundiria.

---

## O problema

Nem toda fonte precisa de cache, e a razão é econômica, não técnica.

- Uma fonte com **conexões ilimitadas** e banda folgada: cachear gasta disco para
  economizar algo que não é escasso.
- Uma fonte com **limite de conexões** ou banda cara: cachear é justamente o que impede
  que dez espectadores do mesmo filme virem dez conexões.

Hoje o sistema faz passthrough para tudo. Depois da Fase 5 ele faria cache de tudo. As
duas escolhas fixas estão erradas para metade dos casos.

---

## A decisão

Três modos, configuráveis **por fonte**, com override **por conteúdo**.

| Modo | Comportamento | Quando usar |
|---|---|---|
| `passthrough` | Nunca grava. Cada espectador abre uma conexão à fonte. | Fonte com conexões ilimitadas e banda barata. Disco vale mais que conexão. |
| `on_demand` *(padrão)* | Grava na primeira vez que alguém pede; os próximos são servidos do disco. | O caso geral. Cacheia só o que é assistido de fato. |
| `always` | Além do `on_demand`, permite pré-carregar conteúdo escolhido. | Acervo pequeno e crítico, ou fonte instável que se quer preservar. |

**Override por conteúdo.** Um filme específico pode ter modo próprio, independente da
fonte — para o caso de "esse aqui eu quero sempre em cache" ou "esse não vale o disco".
A precedência é: conteúdo → fonte → padrão do sistema.

---

## Por que só na Fase 5

Adicionar as colunas agora criaria uma configuração que não faz nada: o administrador
escolheria "sempre baixar" e nada aconteceria. Isso é pior que não ter a opção — ele
confiaria numa proteção inexistente.

O campo entra junto com o mecanismo que o obedece, e com teste que prova cada modo.

---

## Nota sobre Direct Stream no XC_VM

Pergunta que motivou esta decisão: *ativar Direct Stream no XC_VM faz perder qualidade ou
velocidade?*

**Qualidade: nenhuma perda, em nenhum modo.** O VOD Manager nunca transcodifica. Os bytes
que saem são idênticos aos que a fonte enviou — é cópia byte a byte, o mesmo arquivo, o
mesmo codec, o mesmo bitrate. Não há como perder qualidade porque não há reencode.

**Velocidade: depende de qual "direct stream" o XC_VM faz.**

```
XC_VM em PROXY:     cliente → XC_VM → VOD Manager → fonte
XC_VM em REDIRECT:  cliente → XC_VM (só o redirecionamento)
                    cliente → VOD Manager → fonte
```

- Em **proxy**, o XC_VM carrega a banda de todos os clientes, e há um salto de rede a
  mais. O custo é alguns milissegundos no início e o dobro de tráfego na máquina do XC_VM.
- Em **redirect**, o XC_VM só entrega o link e sai do caminho. A máquina dele fica leve,
  mas **quem passa a controlar conexões somos nós** — e é exatamente para isso que servem
  as credenciais de saída com limite de reproduções simultâneas.

Depois que o cache existir, o redirect fica ainda melhor: o segundo espectador do mesmo
filme é servido do nosso disco, sem tocar na fonte, com latência menor que a original.

A recomendação depende do que o administrador quer controlar. Redirect para aliviar a
máquina do XC_VM e concentrar o controle aqui; proxy para manter a contabilidade de
clientes no XC_VM.
