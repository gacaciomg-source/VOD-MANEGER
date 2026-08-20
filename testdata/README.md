# testdata — fixtures de teste

**Nenhum arquivo aqui pode conter credencial, domínio, IP, token ou usuário reais.**

Há um teste no CI (`test/guards`) que varre este diretório inteiro procurando padrões de
credencial e **falha a suíte** se encontrar. Ele é a rede de segurança contra um
vazamento acidental ao colar uma amostra.

## Convenções

- Domínios: `exemplo.tld`, `fonte-a.exemplo.tld`, `fonte-b.exemplo.tld`.
- Usuário/senha em URL: use `usuario`/`senha` literalmente, nunca valores reais.
- IPs: apenas faixas privadas (`10.x`, `192.168.x`) ou documentação (`203.0.113.x`).

## Ao anonimizar uma amostra real

Prefira **substituir por um valor fictício com o mesmo formato** a apagar o campo.
O formato é a informação que interessa: saber que `stream_id` vem como string `"12345"`
e não como número `12345` muda o parser; um campo apagado não conta nada.

## Organização

```
m3u/         listas M3U sintéticas
xtream/      respostas sintéticas de API compatível com Xtream
amostras/    ← coloque aqui as amostras anonimizadas das suas fontes
```

Os fixtures em `m3u/` e `xtream/` são **sintéticos**: foram escritos a partir do formato
público do protocolo, não de nenhuma fonte real. Enquanto `amostras/` estiver vazio, a
compatibilidade com as suas fontes específicas **não está confirmada** — ver
docs/07-contrato-normalizado.md §8.
