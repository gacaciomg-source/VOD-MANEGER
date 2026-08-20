# amostras/ — amostras anonimizadas das suas fontes

Coloque aqui os seis arquivos. O teste de guarda já varre este diretório: se algo escapar
na anonimização, a suíte falha antes de o arquivo chegar a qualquer lugar.

## Nomes esperados

```
01-extinf-filme.txt          uma linha #EXTINF de filme + a linha de URL
02-extinf-episodio.txt       uma linha #EXTINF de episódio + a linha de URL
03-player-api.json           resposta do player_api.php (autenticação/handshake)
04-vod-categories.json       get_vod_categories
05-series.json               get_series
06-series-info.json          get_series_info de UMA série (se disponível)
```

Se algum não existir na sua fonte, não invente: deixe o arquivo de fora e me avise.
A ausência também é informação — significa que o contrato não pode depender daquilo.

## Como anonimizar

**Substitua por valor fictício com o MESMO FORMATO. Não apague.**

O formato é o que interessa. Saber que `stream_id` vem como `"12345"` (string) e não como
`12345` (número) muda o parser; um campo apagado não conta nada.

| O que | Substitua por |
|---|---|
| domínio | `fonte-a.exemplo.tld` |
| usuário | `usuario` |
| senha | `senha` |
| token | uma string de **mesmo comprimento**, ex.: `aaaaaaaaaaaaaaaaaaaaaaaa` |
| IP | `10.0.0.1` |
| e-mail | `pessoa@exemplo.tld` |

**Preserve intactos:** nomes de campo, tipos (string vs. número), estrutura de aninhamento,
ordem das chaves, formato de datas, e a **estrutura do path** da URL — por exemplo, mantenha
`/movie/usuario/senha/12345.mp4` em vez de reduzir para `/12345.mp4`. É exatamente a forma
do path que preciso ver para decidir a questão pendente sobre credencial embutida.

Basta **um exemplo de cada tipo**; não é preciso mandar o catálogo inteiro. Se houver
variação relevante (uma série que numera diferente, um filme sem ano), mande as duas formas.

## O que eu faço quando chegarem

Na ordem aprovada, sem pular etapa:

1. amostras aqui;
2. análise dos campos;
3. `docs/09-campos-das-fontes.md`;
4. classificação Garantido / Opcional / Vendor de cada campo;
5. registro formal de qualquer promoção Vendor → Opcional;
6. testes para as convenções encontradas;
7. ajuste dos parsers;
8. só então HTTP client, scheduler, staging, diff e persistência.

Nenhuma heurística sobre `last_modified`, id numérico no path ou remoção de credencial do
path será implementada sem evidência nestas amostras.
