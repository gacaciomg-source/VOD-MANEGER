// Confere o JavaScript do painel além da sintaxe.
//
// Existe por causa de um erro concreto: `$$('[data-unir]')` virou `$('[data-unir]')` numa
// edição automática, porque em String.replace o `$$` do texto de substituição significa
// "um cifrão literal". O arquivo continuou com sintaxe válida e o build continuou
// passando; a tela de Categorias é que quebrou inteira, com
// "$(...).forEach is not a function".
//
// São verificações estáticas de propósito: o painel não tem DOM aqui. O que dá para
// afirmar sem navegador, afirmamos.
//
// Uso:  node scripts/painel_test.js

const fs = require('fs');
const path = require('path');

const arquivo = path.join(__dirname, '..', 'internal', 'panel', 'assets', 'app.js');
const codigo = fs.readFileSync(arquivo, 'utf8');

// O HTML fixo do painel (menu, modal, cabecalho) tambem declara atributos data-*. Olhar
// so o JS acusaria como orfao todo atributo que mora la — foi o que aconteceu na primeira
// vez que este teste rodou.
const marcacao = codigo + fs.readFileSync(
  path.join(__dirname, '..', 'internal', 'panel', 'assets', 'index.html'), 'utf8');
const linhas = codigo.split('\n');

let falhas = 0;
const ok = m => console.log(`  \x1b[32mok\x1b[0m   ${m}`);
const falha = m => { console.log(`  \x1b[31mFALHA\x1b[0m ${m}`); falhas++; };

console.log('\nPainel — app.js');

// ---------------------------------------------------------------------------
// $ devolve um elemento; $$ devolve uma lista. Chamar forEach no primeiro é o erro
// que motivou este arquivo.
{
  const erradas = [];
  linhas.forEach((linha, i) => {
    if (/(^|[^$])\$\((['"`])\[[^)]*\2\)\s*\.forEach/.test(linha)) {
      erradas.push(`${i + 1}: ${linha.trim()}`);
    }
  });
  erradas.length === 0
    ? ok('nenhum forEach sobre $() — seletor único não é lista')
    : erradas.forEach(l => falha(`forEach sobre $() em ${l}`));
}

// ---------------------------------------------------------------------------
// Um seletor que aponta para um atributo que nenhum HTML produz é um botão morto: não
// dá erro, simplesmente nunca faz nada.
{
  const usados = new Set();
  for (const m of codigo.matchAll(/\$\$\((['"`])\[(data-[a-z-]+)\][^)]*\1\)/g)) {
    usados.add(m[2]);
  }
  const orfaos = [...usados].filter(attr => !new RegExp(`${attr}=`).test(marcacao));
  usados.size > 0 || falha('nenhum seletor $$([data-…]) encontrado; a checagem não rodou');
  orfaos.length === 0
    ? ok(`os ${usados.size} seletores data-* têm HTML correspondente`)
    : orfaos.forEach(a => falha(`$$([${a}]) não casa com nenhum ${a}= no HTML`));
}

// ---------------------------------------------------------------------------
// dataset converte data-unir-cat em unirCat. Escrever dataset.unirCat sem o atributo
// data-unir-cat devolve undefined em silêncio.
{
  const problemas = [];
  for (const m of codigo.matchAll(/dataset\.([a-zA-Z]+)/g)) {
    const emKebab = 'data-' + m[1].replace(/[A-Z]/g, c => '-' + c.toLowerCase());
    if (!new RegExp(`${emKebab}=`).test(marcacao)) problemas.push(`${m[0]} espera ${emKebab}=`);
  }
  [...new Set(problemas)].length === 0
    ? ok('todo dataset.x tem o data-x correspondente')
    : [...new Set(problemas)].forEach(p => falha(p));
}

// ---------------------------------------------------------------------------
// A atualização automática não pode redesenhar por cima de quem está digitando.
{
  const temGuarda = codigo.includes('function atualizacaoAtrapalha');
  const usaClasse = /classList\.contains\(['"]hidden['"]\)/.test(codigo);
  const timersCrus = linhas.filter(l =>
    /setTimeout\(\(\)\s*=>\s*\{?\s*if\s*\(estado\.rota/.test(l) && !l.includes('agendarAtualizacao'));

  temGuarda ? ok('existe guarda contra redesenhar durante digitação')
            : falha('atualizacaoAtrapalha() sumiu');
  usaClasse ? ok('o modal é detectado por classe, como o painel o esconde')
            : falha('a checagem do modal não usa classList');
  timersCrus.length === 0
    ? ok('nenhuma tela se redesenha por temporizador sem passar pelo agendador')
    : timersCrus.forEach(l => falha(`temporizador cru: ${l.trim()}`));
}

console.log();
if (falhas === 0) {
  console.log('\x1b[32mTodas as verificações passaram.\x1b[0m\n');
  process.exit(0);
}
console.log(`\x1b[31m${falhas} verificação(ões) falharam.\x1b[0m\n`);
process.exit(1);
