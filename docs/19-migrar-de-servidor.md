# Migrar para outra máquina

Um comando, rodado no servidor **atual**, leva tudo para o novo.

```bash
sudo ./scripts/migrar.sh --destino root@IP-DO-NOVO-SERVIDOR
```

A senha do SSH é pedida uma vez só — a conexão é reaproveitada nos oito passos.

## O que vai junto

- **O catálogo inteiro, com os mesmos ids.** Filme 12345 continua sendo o filme 12345.
- **A chave de criptografia.** Sem ela, as credenciais das fontes e as senhas dos clientes
  viram bytes ilegíveis no destino. É o item que mais some em migração feita à mão.
- **Os usuários do painel.** Você entra no destino com o mesmo login e a mesma senha.
- **As credenciais de saída, o consumo já contado e as cotas de banda.**
- **As decisões que você tomou:** categorias principais, vínculos de pasta, duplicatas
  ignoradas, matchings travados.

## O que não vai, e por quê

**O endereço.** Os ids são os mesmos, mas o servidor novo tem outro IP. Os links que os
seus clientes já têm apontam para o servidor antigo.

Isso é o que decide se você precisa mexer no XC_VM ou não:

- **Com domínio:** aponte o DNS para o IP novo e pronto. Os links não mudam, o XC_VM não
  percebe nada, ninguém resincroniza.
- **Sem domínio:** o endereço dentro dos links muda de IP. Preservar os ids ajuda, mas o
  host precisa ser trocado onde os links foram cadastrados.

Se a sua meta é *não resincronizar o XC_VM*, o domínio não é um detalhe — é a peça que
faz a migração ser invisível. Configure-o **antes** de migrar, na aba Sistema do painel.

## O que o script não faz de propósito

**Não desliga o servidor atual.** Enquanto você confere o destino, seus clientes continuam
assistindo aqui. Desligar é decisão sua, depois de conferir.

**Não apaga nada na origem.** O backup gerado fica guardado em
`/var/backups/vodmanager/`.

## Os oito passos

1. **Confere antes de começar** — que você está no servidor certo, que o destino responde,
   que é Ubuntu/Debian, e que a chave de criptografia existe. Se o destino já tiver uma
   instalação, exige confirmação digitada antes de substituir.
2. **Backup daqui**, e mede o catálogo (contagem e maior id) para conferir depois.
3. **Leva o código** — clona o repositório no destino.
4. **Leva a chave de criptografia**, *antes* de o instalador rodar. O instalador preserva
   a chave que encontra e só gera uma nova quando não há nenhuma; plantá-la aqui é o que
   faz o destino nascer capaz de ler os dados que vão chegar. A chave vai pela entrada
   padrão, nunca como argumento — argumento aparece no `ps` da máquina inteira.
5. **Instala no destino** — Postgres, Go, compilação, serviço. Leva alguns minutos.
6. **Envia os dados.**
7. **Restaura**, com o serviço parado. Sem `--forcar`: a restauração recusa um backup cuja
   dona é outra chave, e essa recusa é a prova de que o passo 4 funcionou.
8. **Confere** — o serviço responde, e a contagem de conteúdos e o maior id batem com a
   origem. Se não baterem, o script falha e manda não desligar o servidor atual.

## Depois de migrar

Antes de desligar o servidor antigo, confira no novo:

1. entrar no painel;
2. abrir um filme e testar o link de reprodução;
3. testar as fontes — se as credenciais foram junto, elas passam.

Só então desligue o antigo. O backup da migração continua em `/var/backups/vodmanager/`
dos dois lados.

## Se der errado

Toda falha do script deixa a origem intacta, e ele diz isso na tela. A restauração no
destino é uma transação só: ou entra inteira, ou não entra nada.

Os erros mais prováveis:

| Sintoma | Causa |
| --- | --- |
| `não consegui conectar` | porta SSH diferente — use `--porta-ssh` |
| `o destino não é Ubuntu/Debian` | o instalador só cobre apt |
| a restauração recusa por chave | o passo 4 não chegou; rode de novo |
| `os números NÃO batem` | a carga não completou; **não desligue a origem** |

## Testes

`scripts/migrar_test.sh` exercita o script inteiro com dublês de `ssh`, `scp`, `psql` e do
binário — uma migração só roda de verdade no dia em que precisa dar certo, então o ensaio
tem de acontecer em outro lugar.

```bash
bash scripts/migrar_test.sh
```

Cobre: a chave chegar e chegar antes do instalador; a restauração não usar `--forcar`; a
origem não ser tocada; contagem divergente falhar; id divergente falhar; medida vazia não
ser confundida com sucesso; destino fora do ar falhar; e as recusas de entrada.
