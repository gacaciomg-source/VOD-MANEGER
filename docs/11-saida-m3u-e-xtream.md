# VOD Manager — 11. Saída do catálogo: lista M3U e API Xtream

> Fase 7. É o que transforma o catálogo unificado numa URL que o XC_VM — ou qualquer
> aplicativo de IPTV — consome inteira, sem clicar item por item no painel.

---

## O que existe

Duas formas de entregar o mesmo catálogo, escolhidas pelo cliente:

| Endereço | Formato | Quando usar |
|---|---|---|
| `/get.php?username=&password=` | Lista M3U estendida | Qualquer aplicativo que aceite lista. Simples e plano. |
| `/playlist/{usuário}/{senha}` | Idem, com a credencial no caminho | Quando a URL fica mais limpa sem parâmetros. |
| `/player_api.php?username=&password=` | API Xtream | XC_VM e aplicativos que mostram capas, sinopses e pastas. |
| `/xmltv.php` | Guia vazio | Só existe porque alguns clientes tratam 404 aqui como servidor fora do ar. |

Os nomes com `.php` não indicam PHP nenhum: são o que o protocolo Xtream padronizou e o
que todo cliente existente procura. Renomeá-los só funcionaria com clientes escritos
para nós.

### Listas parciais

`&conteudo=filmes` ou `&conteudo=series` limitam o que sai. Útil quando um cliente só
deve receber parte do acervo.

---

## Autenticação: a mesma credencial do streaming

A lista usa a **credencial de saída** que já existia para assistir. Foi uma decisão, não
uma economia de código:

- Quem pode assistir pode listar. Duas credenciais separadas criariam o estado em que um
  cliente cortado ainda enxerga o catálogo, ou o contrário.
- **Revogar corta as duas coisas de uma vez.** Não há meia porta para esquecer aberta.
- O consumo aparece somado por cliente, porque é a mesma linha no banco.

Credencial inexistente, senha errada e credencial revogada devolvem exatamente a mesma
resposta. Distinguir permitiria descobrir usuários válidos por tentativa.

---

## O invariante: a fonte nunca aparece

Todo link da lista aponta para o nosso próprio servidor:

```
http://seu-endereco/movie/{usuário}/{senha}/{id}.mp4
http://seu-endereco/series/{usuário}/{senha}/{id}.mkv
```

Quem escolhe a variante é a camada de transporte, no momento do play — a lista não sabe e
não precisa saber de qual fonte o vídeo virá. Na API Xtream, o campo `direct_source` sai
**vazio de propósito**: preenchido, ele mandaria o cliente buscar o vídeo em outro
endereço, que é exatamente o que o sistema existe para evitar.

Há teste dedicado a isso (`TestListaM3UNuncaExpoeAFonte`): ele varre a lista inteira e
falha se qualquer linha apontar para fora do nosso endereço.

---

## Escala: nada é montado em memória

Uma lista completa deste acervo passa de 270 mil linhas. Montá-la em memória antes de
enviar significaria dezenas de megabytes por cliente conectado — e vários clientes
sincronizando ao mesmo tempo é o caso normal, não a exceção.

Por isso:

- As consultas de exportação entregam por **callback**, uma linha por vez, em vez de
  devolver uma fatia pronta.
- A escrita vai direto para o socket, através de um buffer de 64KB.
- O array JSON da API Xtream também é escrito em fluxo, item a item.
- Um cliente que desconecta no meio interrompe a varredura na hora, em vez de o servidor
  terminar de montar uma resposta que ninguém vai ler.

O custo de memória por cliente conectado é o buffer, não o catálogo.

### Só o que é reproduzível

A exportação omite conteúdo sem variante habilitada e disponível, e categorias que
ficariam vazias. Numa lista de milhares de itens, uma pasta que abre em nada é
indistinguível de um defeito.

---

## Contabilidade por credencial

As colunas **Usos** e **Transferido** do painel estavam sempre zeradas: a função que as
alimenta existia e não era chamada por ninguém. Corrigido.

O consumo agora é acumulado em memória e gravado em lote a cada cinco segundos, em vez de
uma escrita por requisição. A razão é concreta: todos os espectadores de um mesmo cliente
compartilham a mesma linha de credencial, e cada seek de cada player é uma requisição
nova. Sem o lote, com cem pessoas assistindo a contabilidade passaria a disputar bloqueio
com a entrega do vídeo.

A troca: um desligamento abrupto perde no máximo os últimos segundos de contagem. É
deliberado — a contagem serve para o administrador se orientar, não é registro
financeiro. No desligamento normal o acumulado é gravado antes de sair.

Tentativa frustrada também conta como uso. Sem isso, uma credencial que só gera erro
apareceria no painel como se nunca tivesse sido usada — exatamente quando o administrador
precisa notá-la.

---

## No painel

Cada credencial ganhou o botão **Lista**, que entrega:

- o endereço da lista M3U completa;
- os dados para cadastrar como servidor Xtream (servidor, usuário, senha);
- os endereços das listas parciais.

Como a senha não é recuperável — só existe como assinatura no banco —, os links saem com
`SUA_SENHA` no lugar dela, para o administrador substituir pela que anotou. Quem perdeu a
senha usa **Nova senha**: o usuário e os links continuam os mesmos.

O modal avisa quando o endereço público ainda está local, com atalho para a tela de
Configurações — é o mesmo aviso do link de reprodução, pela mesma razão.

---

## O que NÃO existe ainda

- **Canais ao vivo.** `get_live_categories` e `get_live_streams` respondem lista vazia. O
  cliente simplesmente não mostra a aba, em vez de exibir erro.
- **Guia de programação com conteúdo.** O XMLTV sai válido e vazio.
- **Limite de itens por credencial.** Hoje toda credencial recebe o catálogo inteiro. O
  recorte por categoria existe como parâmetro na URL, mas não como permissão gravada — um
  cliente que descubra a URL sem o parâmetro vê tudo.
- **Cache da lista.** Cada requisição relê o catálogo do banco. Com o acervo atual isso é
  rápido; se muitos clientes sincronizarem ao mesmo tempo, vale medir antes de otimizar.
