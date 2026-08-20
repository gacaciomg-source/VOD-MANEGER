# VOD Manager — 13. Dimensionamento da VPS e monitoramento

> Como escolher o tamanho da máquina, e como o painel diz quando trocar.

---

## A tela Sistema

Nova entrada no menu. Mostra CPU, memória, swap, disco e rede da máquina, o tamanho do
banco, as reproduções em andamento — e, acima de tudo, um **veredito interpretado**.

O veredito existe porque número cru não responde a pergunta real. "Memória em 78%" não
diz se é hora de trocar de VPS: em Linux, 78% com cache é saudável; 78% com swap ativa é
problema. A interpretação e a recomendação ficam junto do número.

Três regras que o veredito segue, e que valem explicação:

- **O pior recurso manda.** Uma máquina com disco a 95% não é "ok" porque a CPU está
  ociosa.
- **CPU alta durante sincronização não alarma.** A sincronização é o único trabalho pesado
  de CPU do sistema, e ela termina. Recomendar troca de plano nessa hora faria o
  administrador gastar por um pico que passa sozinho — a recomendação certa é agendar a
  sincronização para fora do horário de pico.
- **Sem medida, o veredito diz que não sabe.** Nunca "está tudo bem". Fora do Linux o
  painel informa que a medição não existe ali, em vez de mostrar zeros que pareceriam
  ociosidade.

A medição de host vem de `/proc` e `statfs`, sem dependência externa — é a mesma fonte que
as bibliotecas de mercado consultam. Em Windows e macOS (ambientes de desenvolvimento)
sobram apenas os números do próprio processo.

CPU e rede são **diferenças entre dois instantes**, por isso a amostragem é periódica (a
cada 5s) e não sob demanda: calculá-las no momento do pedido exigiria segurar a requisição
por um intervalo, ou devolver o número errado.

---

## O que realmente dimensiona a máquina

### 1. Banda — é o que satura primeiro

Hoje a entrega é direta da fonte: cada byte que chega ao espectador **entrou** vindo da
sua fonte. A banda conta duas vezes.

Dez pessoas assistindo a 5 Mbps consomem cerca de **100 Mbps** no total, não 50.

E há uma armadilha que quase ninguém percebe ao contratar: **o limite mensal de tráfego
aperta muito antes da velocidade da porta.**

| Espectadores simultâneos | Saída | Tráfego por hora |
|---|---|---|
| 10 | ~50 Mbps | ~22 GB |
| 50 | ~250 Mbps | ~112 GB |
| 100 | ~500 Mbps | ~225 GB |

Com um plano de **2 TB/mês**, 100 espectadores simultâneos esgotam a franquia em cerca de
**9 horas de uso pleno**. A porta de 1 Gbps nem chega a ser o gargalo. Ao comparar planos,
o número que importa é o tráfego mensal, ou a existência de tráfego ilimitado.

O cache (Fase 5) corta a metade que vem da fonte: o segundo espectador do mesmo filme é
servido do disco, sem tocar na origem.

### 2. Processador — quase não é usado para entregar vídeo

Não há reencode. A entrega é cópia de bytes, que é barata. O processador pesa durante a
**sincronização**, que é trabalho pontual e agendável.

Se a CPU só sobe enquanto sincroniza, a resposta é o horário da sincronização, não um
plano maior.

### 3. Memória — serve ao banco

O que sobra vira cache das consultas do Postgres. Sem folga, a navegação no painel fica
lenta em tudo. Swap em uso é sinal de que já faltou, e nenhum ajuste de configuração
compensa.

### 4. Disco — pouco, por enquanto

O catálogo é texto. Medido no acervo real (16.391 filmes, 8.883 séries, 254.025 episódios,
290.610 variantes): **472 MB** de banco.

Isso muda quando o cache de vídeo existir — aí o disco passa a ser dimensionado pelo
acervo que você quiser manter guardado, e vira o recurso mais caro.

---

## Recomendação

| Uso | vCPU | Memória | Disco | Rede |
|---|---|---|---|---|
| Testes e uso pessoal (até ~10 simultâneos) | 2 | 4 GB | 40 GB SSD | 1 Gbps, tráfego generoso |
| Operação pequena (até ~50 simultâneos) | 4 | 8 GB | 80 GB SSD | 1 Gbps, ≥ 10 TB/mês |
| Alvo declarado (100 simultâneos) | 4–6 | 8–16 GB | 160 GB SSD | 1 Gbps dedicado, tráfego ilimitado ou ≥ 30 TB |

Notas sobre a tabela:

- **A coluna que mais importa é a última.** Errar a CPU custa lentidão; errar o tráfego
  mensal custa o serviço parado no meio do mês.
- **8 GB é o ponto de conforto** para este tamanho de catálogo. Com 4 GB funciona; com
  2 GB o Postgres perde cache e o painel fica lento.
- **Disco: reserve espaço para o cache futuro.** Hoje 40 GB sobram; depois da Fase 5 a
  conta muda completamente.
- Não há benefício em disco NVMe sobre SSD comum enquanto não houver cache — o caminho
  crítico atual é rede, não disco.

---

## Como decidir com medida em vez de palpite

Abra **Sistema** durante o horário de pico, com gente assistindo. É o único momento em que
o número significa alguma coisa. Uma leitura às 3h da manhã não diz nada sobre a
capacidade da máquina.

Se o veredito apontar um recurso, ele já vem com a recomendação específica daquele
recurso — inclusive quando a recomendação é *não* trocar de plano.
