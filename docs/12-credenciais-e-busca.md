# VOD Manager — 12. Credenciais prontas para entregar, e a busca do painel

> Correções e mudanças pedidas depois do primeiro teste real da saída M3U/Xtream.

---

## 1. A senha de saída passou a ser recuperável

**O que havia.** A senha só existia como HMAC: verificável, nunca legível. O painel
mostrava links com `SUA_SENHA` no lugar dela, para o administrador substituir à mão.

**Por que mudou.** Essa é a proteção certa para uma senha que uma *pessoa* escolhe e
memoriza. Não é o caso aqui: esta senha é lida por máquina e precisa ser entregue pronta,
dentro de uma URL, para um cliente que não vai digitar nada. Na prática, irrecuperável
significava o administrador anotando senhas num papel — ou trocando a senha toda vez que
precisasse do link, derrubando quem estivesse assistindo.

**Como ficou.** A senha é guardada em duas formas:

| Forma | Para quê | Onde é usada |
|---|---|---|
| HMAC-SHA256 | Autenticar cada requisição de vídeo | Caminho crítico. Não decifra nada. |
| AES-256-GCM | Montar o link pronto no painel | Só quando o painel pede os links. |

A cifra usa a mesma chave mestra que já protege as credenciais das fontes, com AAD ligado
ao usuário dono da linha — mover o blob de uma credencial para outra no banco produz falha
de decifragem, não uma senha válida.

**O que isso custa, dito claramente.** Quem obtiver um dump do banco *e* a chave de
criptografia consegue ler as senhas de saída. Antes, nem com as duas coisas. É a troca que
torna o sistema utilizável para o que ele existe: entregar acesso a clientes. A chave
continua fora do banco, e o guia de instalação já insiste em guardá-la separada.

**Quem pode ver.** Só papéis de escrita (admin e operator), e cada exibição é registrada
em evento — a mesma regra da URL de origem de uma variante. Um usuário de leitura vê o
catálogo e os links com o marcador, nunca a senha.

**Credenciais antigas.** As criadas antes da migração 0006 não têm a cópia cifrada. O
painel detecta isso e oferece **Nova senha**, em vez de mostrar um link pela metade.

---

## 2. Usuário e senha escolhidos pelo administrador

Os dois campos passaram a ser opcionais na criação. Em branco, a máquina gera com 32 bytes
de entropia; preenchidos, valem o que o administrador escolheu — o caso de quem já vende
acesso e quer manter o login que o cliente conhece.

Aceitam letras, números, ponto, hífen e sublinhado. A restrição não é estética: os dois
viajam **dentro do caminho da URL do vídeo**, e uma barra ou um espaço produziria um link
que falha sem explicação. A senha exige ao menos 8 caracteres.

Usuário repetido devolve 409, com mensagem no campo certo.

---

## 3. Links prontos, sem substituição manual

Duas telas passaram a entregar o endereço completo:

- **Credenciais → Lista**: lista M3U completa, dados de servidor Xtream (servidor,
  usuário, senha), e as listas parciais de filmes e de séries.
- **Qualquer filme ou episódio → Link de reprodução**: um link por credencial ativa, já
  com a senha daquele cliente embutida.

Nos dois casos, aviso visível quando o endereço público ainda é local, com atalho para
Configurações.

---

## 4. A busca do painel: de 8,3s para 0,12s

O administrador relatou a busca lenta, "sumindo" e "voltando ao início". Eram três
defeitos distintos, e o principal não era o que parecia.

### O gargalo real: a contagem de variantes

Medido no acervo real (16.391 filmes, 8.883 séries, 290.610 variantes):

| Busca | Antes | Depois |
|---|---|---|
| `bob` | 3,3s | 0,08s |
| `homem` | 8,2s | 0,02s |
| `a` | 8,3s | 0,12s |
| `a` (séries) | — | 0,13s |

A listagem trazia a contagem de variantes por uma subconsulta com `OR`:

```sql
WHERE (v.target_kind = 'content' AND v.target_id = c.id)
   OR (c.type = 'series' AND v.target_kind = 'episode' AND v.target_id IN (...))
```

Com o `OR`, **nenhum dos lados podia usar índice**. O Postgres varria as 290 mil linhas de
`source_variants` uma vez por item da página — cinquenta itens viravam 14 milhões de linhas
lidas. Substituído por um `CASE` que avalia só o ramo do tipo daquela linha, cada um
indexável.

Foi por isso que o índice trigram sozinho não resolveu: o custo nunca esteve na
comparação de texto.

### Os índices de título (migração 0007)

Ainda assim entraram, porque a busca compara contra dois campos e só um tinha índice
trigram — o `OR` entre um lado indexável e outro não repetiria o mesmo erro conforme o
acervo crescesse.

### A tela piscando e o cursor voltando ao início

A cada tecla, a tela inteira era redesenhada — **incluindo o próprio campo de busca**, com
o cursor dentro dele. O campo era destruído e recriado: o texto parecia sumir e o cursor
voltava para o começo.

Agora a barra de ferramentas é montada uma vez e só a lista é redesenhada. Junto:

- debounce de 300ms, em vez de uma busca por tecla;
- `AbortController` cancelando a requisição anterior, para uma resposta atrasada de um
  termo já apagado não mandar na tela;
- as categorias deixaram de ser recarregadas a cada busca (eram duas requisições por
  tecla, agora uma por entrada na tela).

---

## 5. Troca de senha do painel

Nova seção em **Configurações**. Exige a senha atual — sem isso, uma sessão esquecida
aberta em outro computador bastaria para tomar a conta — e **encerra todas as sessões**,
inclusive a de quem trocou. É de propósito: quem troca a senha por desconfiar de um acesso
não pode deixar a sessão do invasor viva.

Não permite trocar a senha de outra pessoa. Gestão de usuários é outra tela, que ainda não
existe; até lá, cada um troca a própria.

---

## 6. O painel não atualizava depois do restart

Os arquivos embutidos no binário não têm data de modificação, então o navegador não tinha
como saber que mudaram e servia a versão em cache indefinidamente — atualizar o binário
não atualizava a interface. Foi por isso que o botão **Lista** não apareceu no primeiro
teste, mesmo estando no servidor.

Os arquivos do painel passaram a sair com `no-store`. São poucos kilobytes; revalidar
sempre custa menos que a confusão de ver uma tela antiga.

---

## Medições da exportação completa

No acervo real, por loopback:

| Endereço | Tempo | Tamanho |
|---|---|---|
| Lista M3U completa | 12,5s | 75 MB |
| `get_vod_streams` | 0,8s | 5,1 MB |
| `get_series` | 3,3s | 3,2 MB |

A lista completa é dominada pelo volume de dados, não por processamento. É uma operação
que cada cliente faz uma vez por sincronização; se passar a incomodar, o próximo passo é
cache com invalidação por sincronização — mas vale medir com clientes reais antes.
