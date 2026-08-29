/* VOD Manager — painel. JavaScript puro, sem framework e sem etapa de build. */
'use strict';

// ---------------------------------------------------------------------------
// Utilidades
// ---------------------------------------------------------------------------

const $ = (sel, raiz = document) => raiz.querySelector(sel);
const $$ = (sel, raiz = document) => Array.from(raiz.querySelectorAll(sel));

/** Escapa texto vindo da API. Todo dado exibido passa por aqui: títulos de fontes
 *  externas são conteúdo não confiável e não podem virar HTML. */
function esc(v) {
  if (v === null || v === undefined) return '';
  return String(v).replace(/[&<>"']/g, c => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

function num(n) {
  return (n ?? 0).toLocaleString('pt-BR');
}

function dataHora(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (isNaN(d)) return '—';
  return d.toLocaleString('pt-BR', { dateStyle: 'short', timeStyle: 'short' });
}

function tempoRelativo(iso) {
  if (!iso) return 'nunca';
  const diff = (Date.now() - new Date(iso)) / 1000;
  if (isNaN(diff)) return 'nunca';
  if (diff < 60) return 'agora há pouco';
  if (diff < 3600) return `há ${Math.floor(diff / 60)} min`;
  if (diff < 86400) return `há ${Math.floor(diff / 3600)} h`;
  return `há ${Math.floor(diff / 86400)} d`;
}

function aviso(msg, tipo = '') {
  const el = document.createElement('div');
  el.className = 'aviso ' + tipo;
  el.textContent = msg;
  $('#avisos').appendChild(el);
  setTimeout(() => el.remove(), tipo === 'erro' ? 7000 : 4000);
}

// ---------------------------------------------------------------------------
// Cliente da API
// ---------------------------------------------------------------------------

class ErroAPI extends Error {
  constructor(status, corpo) {
    const msg = corpo?.error?.message || corpo?.message || `erro ${status}`;
    super(msg);
    this.status = status;
    this.codigo = corpo?.error?.code || '';
    this.campos = corpo?.error?.fields || [];
    this.corpo = corpo;
  }
}

async function api(caminho, opcoes = {}) {
  const req = { credentials: 'same-origin', headers: {}, ...opcoes };
  if (req.corpo !== undefined) {
    req.headers['Content-Type'] = 'application/json';
    req.body = JSON.stringify(req.corpo);
    delete req.corpo;
  }

  const resp = await fetch('/api/v1' + caminho, req);
  if (resp.status === 204) return null;

  let corpo = null;
  const texto = await resp.text();
  if (texto) {
    try { corpo = JSON.parse(texto); } catch { corpo = { message: texto }; }
  }

  if (!resp.ok) {
    // Sessão expirada em qualquer chamada devolve o usuário para o login, em vez de
    // deixar a tela quebrada com um erro incompreensível.
    if (resp.status === 401 && estado.usuario) {
      estado.usuario = null;
      mostrarLogin();
      aviso('Sua sessão expirou. Entre novamente.', 'erro');
    }
    throw new ErroAPI(resp.status, corpo);
  }
  return corpo;
}

// ---------------------------------------------------------------------------
// Estado
// ---------------------------------------------------------------------------

const estado = {
  usuario: null,
  rota: 'painel',
  parametros: {},
  // ocupado sinaliza que há uma ação do usuário em andamento (testar, sincronizar,
  // excluir). O auto-refresh respeita isso: recarregar a tela no meio de uma ação apaga
  // o botão em "Testando…" e faz parecer que o clique não funcionou.
  ocupado: false,
};

/** Executa uma ação do usuário protegendo-a do auto-refresh. */
async function comAcao(fn) {
  estado.ocupado = true;
  try {
    return await fn();
  } finally {
    estado.ocupado = false;
  }
}

// ---------------------------------------------------------------------------
// Modal
// ---------------------------------------------------------------------------

function abrirModal(titulo, html, aoMontar) {
  $('#modal-titulo').textContent = titulo;
  $('#modal-corpo').innerHTML = html;
  $('#modal').classList.remove('hidden');
  if (aoMontar) aoMontar($('#modal-corpo'));
  const primeiro = $('#modal-corpo input, #modal-corpo select');
  if (primeiro) primeiro.focus();
}

function fecharModal() {
  $('#modal').classList.add('hidden');
  $('#modal-corpo').innerHTML = '';
}

/** Confirmação explícita para ações destrutivas. */
// perguntar pede um texto ao administrador.
//
// Existe em vez do prompt() do navegador porque o prompt nativo é bloqueado por alguns
// navegadores e não combina com o resto do painel — e uma caixa que às vezes não abre é
// pior que uma que sempre abre.
function perguntar(titulo, mensagem, valorInicial = '') {
  return new Promise(resolve => {
    abrirModal(titulo, `
      <p class="discreto">${esc(mensagem)}</p>
      <label><input id="pergunta-valor" value="${esc(valorInicial)}"></label>
      <div class="grupo-botoes">
        <button class="btn" data-acao="cancelar">Cancelar</button>
        <button class="btn btn-primario" data-acao="confirmar">Confirmar</button>
      </div>
    `, corpo => {
      const campo = corpo.querySelector('#pergunta-valor');
      campo.focus();
      campo.select();
      const confirmar = () => {
        const v = campo.value.trim();
        if (!v) return;
        fecharModal();
        resolve(v);
      };
      campo.onkeydown = e => { if (e.key === 'Enter') confirmar(); };
      corpo.querySelector('[data-acao=cancelar]').onclick = () => { fecharModal(); resolve(null); };
      corpo.querySelector('[data-acao=confirmar]').onclick = confirmar;
    });
  });
}

function confirmar(titulo, mensagem, rotuloConfirmar = 'Confirmar') {
  return new Promise(resolve => {
    abrirModal(titulo, `
      <p>${esc(mensagem)}</p>
      <div class="grupo-botoes">
        <button class="btn" data-acao="cancelar">Cancelar</button>
        <button class="btn btn-perigo" data-acao="confirmar">${esc(rotuloConfirmar)}</button>
      </div>
    `, corpo => {
      corpo.querySelector('[data-acao=cancelar]').onclick = () => { fecharModal(); resolve(false); };
      corpo.querySelector('[data-acao=confirmar]').onclick = () => { fecharModal(); resolve(true); };
    });
  });
}

// ---------------------------------------------------------------------------
// Autenticação
// ---------------------------------------------------------------------------

function mostrarLogin() {
  $('#app').classList.add('hidden');
  $('#tela-login').classList.remove('hidden');
  $('#login-senha').value = '';
}

function mostrarApp() {
  $('#tela-login').classList.add('hidden');
  $('#app').classList.remove('hidden');
  $('#rodape-usuario').innerHTML =
    `<b>${esc(estado.usuario.username)}</b>${esc(estado.usuario.role)}`;
}

$('#form-login').onsubmit = async e => {
  e.preventDefault();
  const botao = $('#login-botao');
  const erro = $('#login-erro');
  erro.hidden = true;
  botao.disabled = true;
  botao.textContent = 'Entrando…';
  try {
    const r = await api('/auth/login', {
      method: 'POST',
      corpo: { username: $('#login-usuario').value, password: $('#login-senha').value },
    });
    estado.usuario = r.user;
    mostrarApp();
    navegar();
  } catch (err) {
    erro.textContent = err.status === 429
      ? 'Muitas tentativas. Aguarde um instante e tente de novo.'
      : 'Usuário ou senha inválidos.';
    erro.hidden = false;
  } finally {
    botao.disabled = false;
    botao.textContent = 'Entrar';
  }
};

$('#botao-sair').onclick = async () => {
  try { await api('/auth/logout', { method: 'POST' }); } catch { /* sair é sempre local */ }
  estado.usuario = null;
  mostrarLogin();
};

// ---------------------------------------------------------------------------
// Roteamento (por hash, sem dependência externa)
// ---------------------------------------------------------------------------

const rotas = {
  painel:         { titulo: 'Painel',          render: verPainel },
  fontes:         { titulo: 'Fontes',          render: verFontes },
  filmes:         { titulo: 'Filmes',          render: () => verCatalogo('movie') },
  series:         { titulo: 'Séries',          render: () => verCatalogo('series') },
  conteudo:       { titulo: 'Conteúdo',        render: verConteudo },
  categorias:     { titulo: 'Categorias',      render: verCategorias },
  naoresolvidos:  { titulo: 'Não resolvidos',  render: verNaoResolvidos },
  credenciais:    { titulo: 'Credenciais',     render: verCredenciais },
  streams:        { titulo: 'Reproduções',     render: verStreams },
  duplicatas:     { titulo: 'Duplicatas',      render: verDuplicatas },
  falhas:         { titulo: 'Falhas',          render: verFalhas },
  usuarios:       { titulo: 'Usuários',        render: verUsuarios },
  acervo:         { titulo: 'Acervo',          render: verAcervo },
  sistema:        { titulo: 'Sistema',         render: verSistema },
  configuracoes:  { titulo: 'Configurações',   render: verConfiguracoes },
  sincronizacoes: { titulo: 'Sincronizações',  render: verSincronizacoes },
  eventos:        { titulo: 'Eventos',         render: verEventos },
};

function irPara(hash) { location.hash = hash; }

async function navegar(opcoes) {
  if (!estado.usuario) return;

  // Silencioso: redesenhar sem apagar o que já está na tela.
  //
  // navegar() é usada de dois jeitos bem diferentes. Quando você clica num menu, trocar
  // tudo por "Carregando…" é o certo — a tela anterior não tem mais nada a ver com o
  // destino. Quando é o relógio de uma tela que se atualiza sozinha (Sistema a cada 5s,
  // Reproduções a cada 2s), é o errado: a tela apaga e volta a cada ciclo, e isso é o
  // piscar. O conteúdo antigo fica no lugar até o novo estar pronto.
  const silencioso = opcoes === true || (opcoes && opcoes.silencioso === true);

  const bruto = location.hash.replace(/^#\/?/, '') || 'painel';
  const [nome, ...resto] = bruto.split('/');
  const rota = rotas[nome] ? nome : 'painel';

  estado.rota = rota;
  estado.parametros = { id: resto[0] };

  $('#titulo-pagina').textContent = rotas[rota].titulo;
  $('#acoes-pagina').innerHTML = '';
  $$('#menu a').forEach(a => a.classList.toggle('ativo', a.dataset.rota === rota));
  $('.lateral').classList.remove('aberta');

  if (!silencioso) $('#visao').innerHTML = '<div class="carregando">Carregando…</div>';
  try {
    await rotas[rota].render();
  } catch (err) {
    if (err.status === 401) return;
    $('#visao').innerHTML = `<div class="erro">Não foi possível carregar: ${esc(err.message)}</div>`;
  }
  atualizarSeloNaoResolvidos();
}

window.addEventListener('hashchange', () => navegar());
$('#botao-menu').onclick = () => $('.lateral').classList.toggle('aberta');
$('#modal-fechar').onclick = fecharModal;
$('#modal').onclick = e => { if (e.target.id === 'modal') fecharModal(); };
document.addEventListener('keydown', e => { if (e.key === 'Escape') fecharModal(); });

// atualizacaoAtrapalha diz se agora é um mau momento para redesenhar a tela sozinho.
//
// Redesenhar destrói os campos do formulário junto — e com eles o texto que estava sendo
// digitado, a seleção e o cursor. Foi assim que o campo de domínio se esvaziava sozinho
// enquanto era preenchido. Um modal aberto tem o mesmo problema, com o agravante de que
// ele cobre a tela que o relógio está tentando atualizar.
function atualizacaoAtrapalha() {
  if (estado.ocupado) return true;
  const modal = $('#modal');
  if (modal && !modal.classList.contains('hidden')) return true;
  const foco = document.activeElement;
  if (!foco) return false;
  return ['INPUT', 'TEXTAREA', 'SELECT'].includes(foco.tagName);
}

// agendarAtualizacao repete o desenho da tela enquanto ela continuar sendo a tela aberta.
//
// Se o momento for ruim (alguém digitando, modal aberto), o ciclo é adiado em vez de
// cancelado: a tela volta a se atualizar sozinha assim que o campo perde o foco, sem
// precisar de recarregar.
const relogios = new Map();

function agendarAtualizacao(rota, intervalo) {
  clearTimeout(relogios.get(rota));
  relogios.set(rota, setTimeout(() => {
    relogios.delete(rota);
    if (estado.rota !== rota) return;
    if (atualizacaoAtrapalha()) {
      agendarAtualizacao(rota, intervalo);
      return;
    }
    navegar({ silencioso: true });
  }, intervalo));
}

async function atualizarSeloNaoResolvidos() {
  try {
    const s = await api('/stats/dashboard');
    const selo = $('#selo-naoresolvidos');
    const n = s.catalog.unresolved_items;
    selo.textContent = num(n);
    selo.hidden = n === 0;
  } catch { /* o selo é acessório */ }
}

// ---------------------------------------------------------------------------
// Tela: Painel
// ---------------------------------------------------------------------------

async function verPainel() {
  const [d, orfaos] = await Promise.all([
    api('/stats/dashboard'),
    api('/maintenance/orphan-contents').catch(() => null),
  ]);
  const c = d.catalog;
  const totalOrfaos = orfaos ? orfaos.movies + orfaos.series : 0;

  const metrica = (valor, rotulo, destaque = false) => `
    <div class="metrica ${destaque && valor > 0 ? 'destaque' : ''}">
      <div class="valor">${num(valor)}</div>
      <div class="rotulo">${esc(rotulo)}</div>
    </div>`;

  const semFontes = c.sources === 0;

  $('#visao').innerHTML = `
    ${semFontes ? `
      <div class="cartao">
        <div class="vazio">
          <span class="icone">📡</span>
          <h3>Nenhuma fonte cadastrada</h3>
          <p>Cadastre sua primeira fonte M3U ou Xtream para o VOD Manager montar o catálogo.</p>
          <button class="btn btn-primario" onclick="location.hash='#/fontes'">Cadastrar fonte</button>
        </div>
      </div>` : ''}

    <div class="secao-titulo">Catálogo</div>
    <div class="grade-metricas">
      ${metrica(c.movies, 'Filmes')}
      ${metrica(c.series, 'Séries')}
      ${metrica(c.episodes, 'Episódios')}
      ${metrica(c.categories, 'Categorias')}
    </div>

    <div class="secao-titulo">Origens</div>
    <div class="grade-metricas">
      ${metrica(c.sources, 'Fontes')}
      ${metrica(c.sources_ok, 'Fontes OK')}
      ${metrica(c.variants, 'Variantes')}
      ${metrica(c.unavailable_variants, 'Indisponíveis', true)}
      ${metrica(c.unresolved_items, 'Não resolvidos', true)}
    </div>

    ${totalOrfaos > 0 ? `
      <div class="cartao" style="border-color:#4a3a12">
        <h2>⚠️ ${num(totalOrfaos)} conteúdo(s) sem nenhuma origem</h2>
        <p class="discreto" style="margin:-8px 0 14px">
          Sobraram de fontes que foram excluídas. O sistema nunca apaga conteúdo sozinho —
          isso é proposital, para que nada suma sem você mandar. Mas se forem resíduo,
          você pode limpá-los agora.
          <br>Serão removidos: ${num(orfaos.movies)} filmes, ${num(orfaos.series)} séries,
          ${num(orfaos.episodes)} episódios e ${num(orfaos.empty_categories)} categorias vazias.
          Conteúdos marcados como preservados não são tocados.
        </p>
        <button class="btn btn-perigo" id="limpar-orfaos">Limpar conteúdos sem origem</button>
      </div>` : ''}

    <div class="cartao">
      <h2>Sincronizações recentes</h2>
      ${d.recent_syncs.length ? tabelaRuns(d.recent_syncs) : '<p class="discreto">Nenhuma sincronização executada ainda.</p>'}
    </div>
  `;

  const limpar = $('#limpar-orfaos');
  if (limpar) limpar.onclick = async () => {
    const ok = await confirmar('Limpar conteúdos sem origem',
      `Remover ${num(orfaos.movies)} filmes, ${num(orfaos.series)} séries e ` +
      `${num(orfaos.episodes)} episódios que não têm nenhuma fonte? Esta ação não pode ser desfeita.`,
      'Remover');
    if (!ok) return;
    limpar.disabled = true;
    limpar.textContent = 'Limpando…';
    try {
      const r = await api('/maintenance/orphan-contents/purge', { method: 'POST' });
      aviso(`Removidos ${num(r.movies)} filmes e ${num(r.series)} séries sem origem.`, 'ok');
      navegar();
    } catch (err) {
      aviso('Falha ao limpar: ' + err.message, 'erro');
      limpar.disabled = false;
      limpar.textContent = 'Limpar conteúdos sem origem';
    }
  };
}

function etiquetaEstadoRun(estadoRun) {
  const mapa = {
    succeeded: ['ok', 'concluída'],
    partial:   ['alerta', 'parcial'],
    failed:    ['erro', 'falhou'],
    running:   ['info', 'em andamento'],
    canceled:  ['neutro', 'cancelada'],
  };
  const [classe, rotulo] = mapa[estadoRun] || ['neutro', estadoRun];
  return `<span class="etiqueta ${classe}">${esc(rotulo)}</span>`;
}

function tabelaRuns(runs) {
  return `<div class="tabela-wrap"><table>
    <thead><tr>
      <th>Fonte</th><th>Estado</th><th>Quando</th>
      <th class="numero">Vistos</th><th class="numero">Novos</th>
      <th class="numero">Atualizados</th><th class="numero">Rejeitados</th>
      <th class="numero">Requisições</th>
    </tr></thead>
    <tbody>${runs.map(r => `
      <tr>
        <td>${esc(r.source_name || r.source_id)}</td>
        <td>${etiquetaEstadoRun(r.state)}</td>
        <td class="discreto">${tempoRelativo(r.started_at)}</td>
        <td class="numero">${num(r.items_seen)}</td>
        <td class="numero">${num(r.items_new)}</td>
        <td class="numero">${num(r.items_updated)}</td>
        <td class="numero">${r.items_rejected > 0 ? `<span class="etiqueta alerta">${num(r.items_rejected)}</span>` : '0'}</td>
        <td class="numero">${num(r.requests_made)}</td>
      </tr>`).join('')}
    </tbody></table></div>`;
}

// ---------------------------------------------------------------------------
// Tela: Fontes
// ---------------------------------------------------------------------------

async function verFontes() {
  const [{ sources }, { runs }] = await Promise.all([
    api('/sources'),
    api('/sync/runs?limit=20').catch(() => ({ runs: [] })),
  ]);
  const emAndamento = new Map(runs.filter(r => r.state === 'running').map(r => [r.source_id, r]));

  $('#acoes-pagina').innerHTML = '<button class="btn btn-primario" id="nova-fonte">+ Nova fonte</button>';
  $('#nova-fonte').onclick = () => formularioFonte(null);

  if (!sources.length) {
    $('#visao').innerHTML = `
      <div class="cartao"><div class="vazio">
        <span class="icone">📡</span>
        <h3>Nenhuma fonte cadastrada</h3>
        <p>Uma fonte é de onde o catálogo vem: uma lista M3U ou um painel compatível com Xtream.</p>
        <button class="btn btn-primario" onclick="document.getElementById('nova-fonte').click()">Cadastrar a primeira</button>
      </div></div>`;
    return;
  }

  $('#visao').innerHTML = `
    <p class="discreto" style="margin:0 0 14px">
      Arraste as linhas para mudar a prioridade. A ordem define qual fonte é tentada primeiro.
    </p>
    <div class="tabela-wrap"><table>
      <thead><tr>
        <th style="width:34px"></th><th>Nome</th><th>Tipo</th><th>Estado</th>
        <th>Credencial</th><th>Última sincronização</th><th style="width:1%"></th>
      </tr></thead>
      <tbody id="corpo-fontes">
        ${sources.map(f => linhaFonte(f, emAndamento.get(f.id))).join('')}
      </tbody>
    </table></div>`;

  ligarAcoesFontes(sources, emAndamento);
  ligarArrastarFontes();

  // Uma sincronização em curso muda os números o tempo todo; sem isso a tela mente até
  // o próximo clique.
  if (emAndamento.size > 0 && estado.rota === 'fontes') {
    agendarAtualizacao('fontes', 3000);
  }
}


function linhaFonte(f, runAtiva) {
  const estados = {
    ok: ['ok', 'online'], degraded: ['alerta', 'instável'],
    down: ['erro', 'offline'], disabled: ['neutro', 'desativada'],
    unknown: ['neutro', 'não testada'],
  };
  const [classe, rotulo] = estados[f.status] || ['neutro', f.status];
  return `
    <tr draggable="true" data-id="${f.id}" class="arrastavel">
      <td class="pega">⠿</td>
      <td>
        <b>${esc(f.name)}</b>
        ${!f.enabled ? ' <span class="etiqueta neutro">desabilitada</span>' : ''}
        ${runAtiva ? ` <span class="etiqueta info">sincronizando · ${num(runAtiva.items_seen)} itens</span>` : ''}
        <div class="discreto truncar mono">${esc(f.base_url)}</div>
      </td>
      <td><span class="etiqueta info">${esc(f.kind)}</span></td>
      <td><span class="etiqueta ${classe}">${esc(rotulo)}</span></td>
      <td>${f.has_credentials
            ? '<span class="etiqueta ok">configurada</span>'
            : '<span class="etiqueta neutro">nenhuma</span>'}</td>
      <td class="discreto">${tempoRelativo(f.last_sync_at)}</td>
      <td>
        <div class="grupo-botoes">
          <button class="btn btn-mini" data-acao="testar"     data-id="${f.id}">Testar</button>
          <button class="btn btn-mini btn-primario" data-acao="sincronizar" data-id="${f.id}">
            ${runAtiva ? 'Ver progresso' : 'Sincronizar'}</button>
          <button class="btn btn-mini" data-acao="editar"     data-id="${f.id}">Editar</button>
          <button class="btn btn-mini" data-acao="credencial" data-id="${f.id}">Credencial</button>
          <button class="btn btn-mini btn-perigo" data-acao="excluir" data-id="${f.id}">Excluir</button>
        </div>
      </td>
    </tr>`;
}

function ligarAcoesFontes(sources, emAndamento = new Map()) {
  const porID = id => sources.find(f => String(f.id) === String(id));

  $$('#corpo-fontes [data-acao]').forEach(botao => {
    botao.onclick = async () => {
      const id = botao.dataset.id;
      const fonte = porID(id);
      const original = botao.textContent;

      switch (botao.dataset.acao) {
        case 'editar':
          return formularioFonte(fonte);

        case 'credencial':
          return formularioCredencial(fonte);

        case 'testar': {
          botao.disabled = true; botao.textContent = 'Testando…';
          await comAcao(async () => {
            try {
              const r = await api(`/sources/${id}/test`, { method: 'POST' });
              // O resultado abre em janela: um teste que leva vários segundos merece uma
              // resposta que fica na tela, não um aviso que some sozinho.
              abrirModal(`Teste — ${fonte.name}`, `
                <div class="${r.ok ? '' : 'erro'}" style="${r.ok ? 'color:var(--ok)' : ''}">
                  <b style="font-size:16px">${r.ok ? '✓ A fonte respondeu' : '✕ A fonte não respondeu'}</b>
                  <div style="margin-top:6px">${esc(r.detail)}</div>
                </div>
                ${!r.ok ? `<p class="dica">
                    Confira a URL base e a credencial. Para Xtream, a URL base é só o
                    endereço do servidor — sem /player_api.php e sem usuário e senha.
                  </p>` : ''}
                <div class="grupo-botoes">
                  <button class="btn" data-acao="fechar">Fechar</button>
                </div>`, corpo => {
                corpo.querySelector('[data-acao=fechar]').onclick = () => { fecharModal(); navegar(); };
              });
            } catch (err) {
              aviso('Falha ao testar: ' + err.message, 'erro');
              botao.disabled = false;
              botao.textContent = original;
            }
          });
          return;
        }

        case 'sincronizar': {
          // Se já há uma execução desta fonte rodando, apenas acompanhamos — o servidor
          // devolve a execução existente em vez de recusar.
          const jaRodando = emAndamento.get(fonte.id);
          if (jaRodando) {
            acompanharSincronizacao(jaRodando.id, fonte.name);
            return;
          }
          botao.disabled = true; botao.textContent = 'Iniciando…';
          await comAcao(async () => {
            try {
              const run = await api(`/sources/${id}/sync`, { method: 'POST' });
              acompanharSincronizacao(run.id, fonte.name);
            } catch (err) {
              aviso('Não foi possível iniciar: ' + err.message, 'erro');
              botao.disabled = false; botao.textContent = original;
            }
          });
          return;
        }

        case 'excluir': {
          const ok = await confirmar(
            'Excluir fonte',
            `Excluir "${fonte.name}"? Todas as variantes vindas dela serão removidas do catálogo. ` +
            `Conteúdos que só existiam nesta fonte ficarão sem nenhuma origem.`,
            'Excluir');
          if (!ok) return;

          // Excluir uma fonte grande remove milhares de linhas e pode demorar. Sem
          // travar todos os botões, um duplo clique dispara DELETEs concorrentes que
          // ficam bloqueando uns aos outros no banco.
          const botoes = $$('#corpo-fontes button');
          botoes.forEach(b => { b.disabled = true; });
          botao.textContent = 'Excluindo…';
          await comAcao(async () => {
            try {
              await api(`/sources/${id}`, { method: 'DELETE' });
              aviso('Fonte excluída.', 'ok');
            } catch (err) {
              aviso('Falha ao excluir: ' + err.message, 'erro');
              botoes.forEach(b => { b.disabled = false; });
              botao.textContent = original;
            }
          });
          navegar();
          return;
        }
      }
    };
  });
}

/**
 * Acompanha uma sincronização em andamento.
 *
 * A sincronização roda no servidor e continua mesmo se esta aba for fechada; o painel
 * apenas consulta o progresso. Por isso o modal pode ser fechado sem cancelar nada.
 */
function acompanharSincronizacao(runID, nomeFonte) {
  let parar = false;

  abrirModal(`Sincronizando ${nomeFonte}`, `
    <div class="progresso"><div class="progresso-barra indeterminada"></div></div>
    <div id="sinc-estado" class="discreto">Conectando à fonte…</div>
    <div class="grade-metricas" id="sinc-contadores"></div>
    <p class="dica">
      A sincronização roda no servidor. Você pode fechar esta janela ou até o navegador —
      ela continua e o resultado aparece em Sincronizações.
    </p>
    <div class="grupo-botoes">
      <button class="btn" data-acao="fechar">Fechar</button>
    </div>
  `, corpo => {
    corpo.querySelector('[data-acao=fechar]').onclick = () => { parar = true; fecharModal(); navegar(); };
  });

  const contador = (valor, rotulo) =>
    `<div class="metrica"><div class="valor">${num(valor)}</div><div class="rotulo">${rotulo}</div></div>`;

  (async function acompanhar() {
    const inicio = Date.now();
    while (!parar) {
      await new Promise(r => setTimeout(r, 1000));
      if (parar || $('#modal').classList.contains('hidden')) return;

      let run;
      try {
        run = await api('/sync/runs/' + runID);
      } catch {
        continue; // uma consulta que falha não derruba o acompanhamento
      }

      const contadores = $('#sinc-contadores');
      const estadoEl = $('#sinc-estado');
      if (!contadores || !estadoEl) return; // o modal foi fechado

      contadores.innerHTML =
        contador(run.items_seen, 'Processados') +
        contador(run.items_new, 'Novos') +
        contador(run.items_updated, 'Atualizados') +
        contador(run.items_unchanged, 'Inalterados') +
        (run.items_rejected ? contador(run.items_rejected, 'Não resolvidos') : '');

      const segundos = Math.round((Date.now() - inicio) / 1000);
      const velocidade = segundos > 0 ? Math.round(run.items_seen / segundos) : 0;

      if (run.state === 'running') {
        estadoEl.textContent =
          `Em andamento — ${segundos}s decorridos` + (velocidade ? `, ~${num(velocidade)} itens/s` : '');
        continue;
      }

      // Terminou.
      const barra = $('.progresso-barra');
      if (barra) {
        barra.classList.remove('indeterminada');
        barra.style.width = '100%';
        barra.classList.add(run.state === 'succeeded' ? 'ok' : 'alerta');
      }
      estadoEl.innerHTML = `${etiquetaEstadoRun(run.state)} em ${segundos}s`;
      if (run.error_message) {
        estadoEl.innerHTML += `<div class="erro" style="margin-top:8px">${esc(run.error_message)}</div>`;
      }
      aviso(`${nomeFonte}: ${num(run.items_seen)} itens processados, ${num(run.items_new)} novos.`,
            run.state === 'succeeded' ? 'ok' : 'erro');
      atualizarSeloNaoResolvidos();
      return;
    }
  })();
}

/** Reordenação por arrastar. A ordem resultante vira a prioridade das fontes. */
function ligarArrastarFontes() {
  const corpo = $('#corpo-fontes');
  if (!corpo) return;
  let origem = null;

  corpo.querySelectorAll('tr').forEach(linha => {
    linha.ondragstart = () => { origem = linha; linha.classList.add('arrastando'); };
    linha.ondragend = async () => {
      linha.classList.remove('arrastando');
      corpo.querySelectorAll('tr').forEach(l => l.classList.remove('alvo-solta'));
      const ids = Array.from(corpo.querySelectorAll('tr')).map(l => Number(l.dataset.id));
      try {
        await api('/sources/reorder', { method: 'POST', corpo: { ids } });
        aviso('Prioridade atualizada.', 'ok');
        navegar();
      } catch (err) {
        aviso('Falha ao reordenar: ' + err.message, 'erro');
        navegar();
      }
    };
    linha.ondragover = e => {
      e.preventDefault();
      if (!origem || origem === linha) return;
      linha.classList.add('alvo-solta');
      const meio = linha.getBoundingClientRect().top + linha.offsetHeight / 2;
      corpo.insertBefore(origem, e.clientY < meio ? linha : linha.nextSibling);
    };
    linha.ondragleave = () => linha.classList.remove('alvo-solta');
  });
}

function formularioFonte(fonte) {
  const editando = !!fonte;
  const f = fonte || {
    name: '', description: '', kind: 'xtream', base_url: '', enabled: true,
    sync_interval_minutes: 1440, max_connections: 4, max_concurrent_downloads: 2,
    request_budget: 5000,
    // Desligado numa fonte nova, e é escolha: guardar consome disco, e ninguém deve
    // descobrir que a decisão foi tomada por ele ao ver a partição cheia.
    cache_habilitado: false,
  };

  abrirModal(editando ? `Editar ${f.name}` : 'Nova fonte', `
    <label>Nome <input id="f-nome" value="${esc(f.name)}" required></label>
    <label>Tipo
      <select id="f-tipo" ${editando ? 'disabled' : ''}>
        <option value="xtream" ${f.kind === 'xtream' ? 'selected' : ''}>Xtream (painel com API)</option>
        <option value="m3u"    ${f.kind === 'm3u' ? 'selected' : ''}>M3U (lista)</option>
      </select>
    </label>
    ${editando ? '<p class="dica">O tipo não pode ser alterado: mudá-lo invalidaria todo o catálogo já importado desta fonte.</p>' : ''}
    <label>URL base
      <input id="f-url" value="${esc(f.base_url)}" placeholder="http://servidor.exemplo:8080" required>
    </label>
    <p class="dica" id="dica-url"></p>
    <label>Descrição <input id="f-desc" value="${esc(f.description)}"></label>
    <div class="linha-campos">
      <label>Sincronizar a cada (minutos)
        <input id="f-intervalo" type="number" min="1" value="${f.sync_interval_minutes}">
      </label>
      <label>Máx. de conexões
        <input id="f-conexoes" type="number" min="1" value="${f.max_connections}">
      </label>
    </div>
    <div class="linha-campos">
      <label>Máx. de downloads simultâneos
        <input id="f-downloads" type="number" min="1" value="${f.max_concurrent_downloads}">
      </label>
      <label>Teto de requisições por sincronização
        <input id="f-orcamento" type="number" min="1" value="${f.request_budget}">
      </label>
    </div>
    <p class="dica">O teto protege a fonte: ao atingi-lo, a sincronização é marcada como parcial em vez de continuar disparando requisições.</p>
    <label style="flex-direction:row;align-items:center;gap:8px">
      <input type="checkbox" id="f-ativa" ${f.enabled ? 'checked' : ''}> Fonte habilitada
    </label>
    <label style="flex-direction:row;align-items:center;gap:8px">
      <input type="checkbox" id="f-cache" ${f.cache_habilitado ? 'checked' : ''}>
      Guardar o conteúdo desta fonte no acervo
    </label>
    <p class="dica">
      Quando alguém assistir, o vídeo é copiado para o acervo e as próximas reproduções
      saem de lá — sem comprar a banda da fonte de novo.
      <br>
      Vale a pena nas fontes que cobram por banda ou são lentas, e não vale nas que trocam
      de link toda semana: a cópia envelheceria mal.
      <br>
      <b>Exige também a chave geral</b>, em Configurações → Armazenamento de mídia.
    </p>
    <div class="erro" id="f-erro" hidden></div>
    <div class="grupo-botoes">
      <button class="btn" data-acao="cancelar">Cancelar</button>
      <button class="btn btn-primario" data-acao="salvar">${editando ? 'Salvar' : 'Criar fonte'}</button>
    </div>
  `, corpo => {
    const dica = corpo.querySelector('#dica-url');
    const tipo = corpo.querySelector('#f-tipo');
    const atualizarDica = () => {
      dica.textContent = tipo.value === 'xtream'
        ? 'Informe só o endereço do servidor, sem /player_api.php e sem usuário e senha — eles vão na credencial.'
        : 'Informe a URL completa da lista .m3u, ou o endereço do servidor se ele usar get.php com credencial.';
    };
    tipo.onchange = atualizarDica;
    atualizarDica();

    corpo.querySelector('[data-acao=cancelar]').onclick = fecharModal;
    corpo.querySelector('[data-acao=salvar]').onclick = async botao => {
      const erro = corpo.querySelector('#f-erro');
      erro.hidden = true;

      const dados = {
        name: corpo.querySelector('#f-nome').value.trim(),
        description: corpo.querySelector('#f-desc').value.trim(),
        base_url: corpo.querySelector('#f-url').value.trim(),
        enabled: corpo.querySelector('#f-ativa').checked,
        sync_interval_minutes: Number(corpo.querySelector('#f-intervalo').value),
        max_connections: Number(corpo.querySelector('#f-conexoes').value),
        max_concurrent_downloads: Number(corpo.querySelector('#f-downloads').value),
        cache_habilitado: corpo.querySelector('#f-cache').checked,
      };
      if (!editando) dados.kind = tipo.value;

      const alvo = botao.target;
      alvo.disabled = true;
      try {
        if (editando) {
          await api(`/sources/${f.id}`, { method: 'PATCH', corpo: dados });
        } else {
          await api('/sources', { method: 'POST', corpo: dados });
        }
        fecharModal();
        aviso(editando ? 'Fonte atualizada.' : 'Fonte criada. Agora configure a credencial, se houver.', 'ok');
        navegar();
      } catch (err) {
        erro.textContent = err.campos?.length
          ? `Campos inválidos: ${err.campos.join(', ')}`
          : err.message;
        erro.hidden = false;
        alvo.disabled = false;
      }
    };
  });
}

function formularioCredencial(fonte) {
  abrirModal(`Credencial — ${fonte.name}`, `
    <p class="discreto">
      A credencial é cifrada antes de ir para o banco e nunca é devolvida por nenhuma resposta
      da API, nem para o administrador. Para trocá-la, grave uma nova.
    </p>
    <label>Usuário na fonte <input id="c-usuario" autocomplete="off"></label>
    <label>Senha na fonte <input id="c-senha" type="password" autocomplete="new-password" required></label>
    <div class="erro" id="c-erro" hidden></div>
    <div class="grupo-botoes">
      ${fonte.has_credentials ? '<button class="btn btn-perigo" data-acao="remover">Remover credencial</button>' : ''}
      <button class="btn" data-acao="cancelar">Cancelar</button>
      <button class="btn btn-primario" data-acao="salvar">Gravar</button>
    </div>
  `, corpo => {
    corpo.querySelector('[data-acao=cancelar]').onclick = fecharModal;

    const remover = corpo.querySelector('[data-acao=remover]');
    if (remover) remover.onclick = async () => {
      try {
        await api(`/sources/${fonte.id}/credentials`, { method: 'DELETE' });
        fecharModal();
        aviso('Credencial removida.', 'ok');
        navegar();
      } catch (err) {
        aviso('Falha ao remover: ' + err.message, 'erro');
      }
    };

    corpo.querySelector('[data-acao=salvar]').onclick = async e => {
      const erro = corpo.querySelector('#c-erro');
      erro.hidden = true;
      e.target.disabled = true;
      try {
        await api(`/sources/${fonte.id}/credentials`, {
          method: 'PUT',
          corpo: {
            username: corpo.querySelector('#c-usuario').value,
            password: corpo.querySelector('#c-senha').value,
          },
        });
        fecharModal();
        aviso('Credencial gravada e cifrada.', 'ok');
        navegar();
      } catch (err) {
        erro.textContent = err.message;
        erro.hidden = false;
        e.target.disabled = false;
      }
    };
  });
}

// ---------------------------------------------------------------------------
// Tela: Catálogo (filmes e séries)
// ---------------------------------------------------------------------------

const filtroCatalogo = { movie: { q: '', offset: 0, categoria: '' }, series: { q: '', offset: 0, categoria: '' } };

// buscaEmVoo cancela a requisição anterior quando uma nova é disparada.
//
// Sem isso, digitar depressa deixa várias buscas correndo em paralelo, e a que voltar por
// último manda na tela — que pode ser a de um termo já apagado.
let buscaEmVoo = null;

async function verCatalogo(tipo) {
  const f = filtroCatalogo[tipo];
  const { categories } = await api('/categories');
  const doTipo = categories.filter(c => c.content_type === tipo);

  // A barra de ferramentas é montada UMA vez. Recriá-la a cada tecla destruía o campo de
  // busca com o cursor dentro dele — era isso que fazia o texto sumir e o cursor voltar
  // para o começo.
  $('#visao').innerHTML = `
    <div class="barra-ferramentas">
      <input type="search" id="busca" placeholder="Buscar por título…" value="${esc(f.q)}"
             autocomplete="off" spellcheck="false">
      <select id="filtro-categoria">
        <option value="">Todas as categorias</option>
        ${doTipo.map(c => `<option value="${c.id}" ${String(f.categoria) === String(c.id) ? 'selected' : ''}>
            ${esc(c.name)} (${num(c.content_count)})</option>`).join('')}
      </select>
      <span class="discreto" id="contagem-catalogo"></span>
    </div>
    <div id="lista-catalogo"><div class="carregando">Carregando…</div></div>`;

  const campo = $('#busca');
  campo.focus();
  // O cursor vai para o fim do que já estava digitado, não para o começo.
  campo.setSelectionRange(campo.value.length, campo.value.length);

  let temporizador;
  campo.oninput = () => {
    clearTimeout(temporizador);
    // 300ms: tempo suficiente para uma palavra inteira ser digitada sem disparar uma
    // busca por letra, e curto o bastante para não parecer travado.
    temporizador = setTimeout(() => {
      f.q = campo.value.trim();
      f.offset = 0;
      carregarListaCatalogo(tipo);
    }, 300);
  };
  $('#filtro-categoria').onchange = e => {
    f.categoria = e.target.value;
    f.offset = 0;
    carregarListaCatalogo(tipo);
  };

  await carregarListaCatalogo(tipo);
}

// carregarListaCatalogo redesenha SÓ a lista, preservando a barra de busca e o foco.
async function carregarListaCatalogo(tipo) {
  const f = filtroCatalogo[tipo];
  const lista = $('#lista-catalogo');
  if (!lista) return;

  if (buscaEmVoo) buscaEmVoo.abort();
  const controle = new AbortController();
  buscaEmVoo = controle;

  let pagina;
  try {
    pagina = await api(
      `/contents?type=${tipo}&limit=50&offset=${f.offset}` +
      (f.q ? `&q=${encodeURIComponent(f.q)}` : '') +
      (f.categoria ? `&category_id=${f.categoria}` : ''),
      { signal: controle.signal });
  } catch (err) {
    // Cancelamento é o caminho normal quando o usuário continua digitando.
    if (err.name === 'AbortError') return;
    lista.innerHTML = `<div class="cartao"><div class="erro">${esc(err.message)}</div></div>`;
    return;
  }
  // Uma resposta que chegou depois de já termos disparado outra busca não manda na tela.
  if (buscaEmVoo !== controle) return;
  buscaEmVoo = null;

  const contagem = $('#contagem-catalogo');
  if (contagem) {
    contagem.textContent = `${num(pagina.total)} ${tipo === 'movie' ? 'filmes' : 'séries'}`;
  }

  if (!pagina.items.length) {
    lista.innerHTML = `<div class="cartao"><div class="vazio">
      <span class="icone">${tipo === 'movie' ? '🎞️' : '📺'}</span>
      <h3>${f.q || f.categoria ? 'Nada encontrado com esse filtro' : 'Catálogo vazio'}</h3>
      <p>${f.q || f.categoria
            ? 'Tente outro termo ou limpe os filtros.'
            : 'Cadastre uma fonte e execute uma sincronização para popular o catálogo.'}</p>
    </div></div>`;
    return;
  }

  lista.innerHTML = `
    <div class="tabela-wrap"><table>
      <thead><tr>
        <th style="width:56px"></th><th>Título</th><th>Ano</th>
        <th class="numero">${tipo === 'movie' ? 'Fontes' : 'Variantes'}</th>
        <th>Estado</th>
      </tr></thead>
      <tbody>${pagina.items.map(c => `
        <tr class="linha-clicavel" data-id="${c.id}">
          <td>${c.poster_url
                 ? `<img class="cartaz" src="${esc(c.poster_url)}" alt="" loading="lazy"
                      onerror="this.outerHTML='<div class=\'sem-cartaz\'>🎬</div>'">`
                 : '<div class="sem-cartaz">🎬</div>'}</td>
          <td><b>${esc(c.title)}</b></td>
          <td class="discreto">${c.year ?? '—'}</td>
          <td class="numero">${c.variant_count > 1
               ? `<span class="etiqueta ok">${num(c.variant_count)}</span>`
               : num(c.variant_count)}</td>
          <td>${c.status === 'active'
               ? '<span class="etiqueta ok">ativo</span>'
               : `<span class="etiqueta alerta">${esc(c.status)}</span>`}</td>
        </tr>`).join('')}
      </tbody>
    </table></div>
    ${paginacao(pagina)}`;

  $$('#lista-catalogo tr[data-id]').forEach(tr => {
    tr.onclick = () => irPara('#/conteudo/' + tr.dataset.id);
  });
  const anterior = $('#pag-anterior'), proximo = $('#pag-proximo');
  if (anterior) anterior.onclick = () => {
    f.offset = Math.max(0, f.offset - pagina.limit);
    carregarListaCatalogo(tipo);
  };
  if (proximo) proximo.onclick = () => {
    f.offset += pagina.limit;
    carregarListaCatalogo(tipo);
  };
}

function paginacao(pagina) {
  const fim = pagina.offset + pagina.items.length;
  if (pagina.total <= pagina.limit) return '';
  return `<div class="paginacao">
    <button class="btn btn-mini" id="pag-anterior" ${pagina.offset === 0 ? 'disabled' : ''}>← Anterior</button>
    <span>${num(pagina.offset + 1)}–${num(fim)} de ${num(pagina.total)}</span>
    <button class="btn btn-mini" id="pag-proximo" ${fim >= pagina.total ? 'disabled' : ''}>Próximo →</button>
  </div>`;
}

// ---------------------------------------------------------------------------
// Tela: detalhe de um conteúdo
// ---------------------------------------------------------------------------

async function verConteudo() {
  const d = await api('/contents/' + estado.parametros.id);
  const c = d.content;

  $('#titulo-pagina').textContent = c.title;
  $('#acoes-pagina').innerHTML =
    `<button class="btn btn-primario" id="links-reproducao">Link de reprodução</button>
     <button class="btn" onclick="history.back()">← Voltar</button>`;
  $('#links-reproducao').onclick = () => abrirLinksReproducao('contents', c.id, c.title);

  const ficha = [
    ['Tipo', c.type === 'movie' ? 'Filme' : 'Série'],
    ['Ano', c.year ?? '—'],
    ['Título normalizado', `<span class="mono">${esc(c.normalized_title)}</span>`],
    ['TMDB', c.tmdb_id ?? '—'],
    ['IMDb', c.imdb_id ?? '—'],
    ['Estado', c.status],
    ['Acessos', num(c.access_count)],
  ];

  $('#visao').innerHTML = `
    <div class="cartao" style="margin-top:0;display:flex;gap:20px;flex-wrap:wrap">
      ${c.poster_url
        ? `<img src="${esc(c.poster_url)}" alt="" style="width:120px;border-radius:8px"
             onerror="this.remove()">`
        : ''}
      <div style="flex:1;min-width:260px">
        <h2 style="font-size:19px">${esc(c.title)}</h2>
        ${c.plot ? `<p class="discreto" style="margin:8px 0 14px">${esc(c.plot)}</p>` : ''}
        <div class="tabela-wrap" style="border:none;background:none">
          <table><tbody>
            ${ficha.map(([k, v]) => `<tr>
                <td class="discreto" style="width:170px;border:none">${esc(k)}</td>
                <td style="border:none">${v}</td></tr>`).join('')}
          </tbody></table>
        </div>
      </div>
    </div>

    ${c.type === 'series' ? blocoTemporadas(d.seasons) : blocoVariantes(d.variants)}
  `;

  ligarAcoesVariantes();
  $$('[data-episodio]').forEach(el => {
    el.onclick = () => abrirEpisodio(el.dataset.episodio);
  });
}

function blocoVariantes(variantes) {
  if (!variantes || !variantes.length) {
    return `<div class="cartao"><div class="vazio">
      <span class="icone">🔌</span>
      <h3>Nenhuma origem</h3>
      <p>Este conteúdo não tem nenhuma fonte ativa. Ele veio de uma fonte que foi removida
         ou o item desapareceu de todas as fontes.</p>
    </div></div>`;
  }
  return `<div class="cartao">
    <h2>Origens (${variantes.length})</h2>
    <p class="discreto" style="margin:-8px 0 14px">
      Ordenadas pela prioridade da fonte. A primeira disponível é a que será usada.
    </p>
    ${tabelaVariantes(variantes)}
  </div>`;
}

function tabelaVariantes(variantes) {
  return `<div class="tabela-wrap"><table>
    <thead><tr>
      <th>Fonte</th><th>Título declarado</th><th>Container</th>
      <th>Qualidade / idioma</th><th>Estado</th><th style="width:1%"></th>
    </tr></thead>
    <tbody>${variantes.map((v, i) => `
      <tr>
        <td>
          <b>${esc(v.source_name)}</b>
          ${i === 0 && v.available ? ' <span class="etiqueta ok">será usada</span>' : ''}
          <div class="discreto">prioridade ${v.source_priority}</div>
        </td>
        <td class="truncar">${esc(v.declared_title)}</td>
        <td>${v.container_ext ? `<span class="etiqueta neutro">${esc(v.container_ext)}</span>` : '—'}</td>
        <td>${[...(v.quality_tags || []), ...(v.language_tags || [])]
              .map(t => `<span class="etiqueta neutro">${esc(t)}</span>`).join(' ') || '—'}</td>
        <td>${v.available
              ? '<span class="etiqueta ok">disponível</span>'
              : '<span class="etiqueta erro">indisponível</span>'}
            ${!v.enabled ? '<span class="etiqueta neutro">desativada</span>' : ''}</td>
        <td><button class="btn btn-mini" data-origem="${v.id}">Link final</button></td>
      </tr>`).join('')}
    </tbody></table></div>`;
}

async function copiar(texto, botao) {
  try {
    await navigator.clipboard.writeText(texto);
  } catch {
    // Fora de HTTPS o clipboard pode estar bloqueado: recorre à seleção manual.
    const area = document.createElement('textarea');
    area.value = texto;
    document.body.appendChild(area);
    area.select();
    try { document.execCommand('copy'); } catch { /* sem alternativa */ }
    area.remove();
  }
  if (botao) {
    const antes = botao.textContent;
    botao.textContent = '✓ Copiado';
    setTimeout(() => { botao.textContent = antes; }, 1500);
  }
}

function ligarAcoesVariantes() {
  $$('[data-origem]').forEach(botao => {
    botao.onclick = async () => {
      const antes = botao.textContent;
      botao.disabled = true;
      botao.textContent = 'Resolvendo…';
      try {
        const r = await api(`/variants/${botao.dataset.origem}/origin-url`);
        abrirModal('Link final na fonte', `
          <p class="discreto">
            Este é o link exato que o VOD Manager vai requisitar da fonte quando alguém
            pedir este conteúdo — já com as credenciais aplicadas.
          </p>
          <textarea class="mono" rows="4" readonly id="url-origem"
                    onclick="this.select()">${esc(r.origin_url)}</textarea>
          <div class="erro">
            Contém a senha da sua fonte. Não compartilhe: quem tiver este link tem acesso à
            sua conta na fonte. Cada consulta como esta fica registrada nos eventos.
          </div>
          <div class="grupo-botoes">
            <button class="btn" data-acao="fechar">Fechar</button>
            <button class="btn btn-primario" data-acao="copiar">Copiar link</button>
          </div>`, corpo => {
          corpo.querySelector('[data-acao=fechar]').onclick = fecharModal;
          corpo.querySelector('[data-acao=copiar]').onclick = e =>
            copiar(r.origin_url, e.target);
        });
      } catch (err) {
        aviso('Não foi possível resolver a URL: ' + err.message, 'erro');
      } finally {
        botao.disabled = false;
        botao.textContent = antes;
      }
    };
  });
}

function blocoTemporadas(temporadas) {
  if (!temporadas || !temporadas.length) {
    return `<div class="cartao"><div class="vazio">
      <span class="icone">📺</span><h3>Nenhuma temporada</h3>
      <p>Esta série ainda não tem episódios importados.</p>
    </div></div>`;
  }
  return temporadas.map(t => `
    <div class="cartao">
      <h2>Temporada ${t.season_number} <span class="discreto">— ${t.episodes.length} episódios</span></h2>
      ${t.episodes.length ? `
        <div class="tabela-wrap"><table>
          <thead><tr><th style="width:60px">Ep.</th><th>Título</th><th class="numero">Origens</th><th>Estado</th></tr></thead>
          <tbody>${t.episodes.map(e => `
            <tr class="linha-clicavel" data-episodio="${e.id}">
              <td class="numero">${e.episode_number}</td>
              <td>${esc(e.title) || '<span class="discreto">sem título</span>'}</td>
              <td class="numero">${e.variant_count > 1
                   ? `<span class="etiqueta ok">${num(e.variant_count)}</span>` : num(e.variant_count)}</td>
              <td>${e.status === 'active'
                   ? '<span class="etiqueta ok">ativo</span>'
                   : `<span class="etiqueta alerta">${esc(e.status)}</span>`}</td>
            </tr>`).join('')}
          </tbody></table></div>`
        : '<p class="discreto">Nenhum episódio nesta temporada.</p>'}
    </div>`).join('');
}

/**
 * Mostra os links de SAÍDA de um conteúdo — os que apontam para o VOD Manager.
 *
 * Não confundir com o "Link final na fonte": aquele é o que NÓS pedimos à fonte e contém
 * a senha dela. Este é o que o XC_VM (ou um player) pede a NÓS, e nunca revela a origem.
 */
async function abrirLinksReproducao(recurso, id, titulo) {
  abrirModal(`Link de reprodução — ${titulo}`, '<div class="carregando">Montando os links…</div>');
  try {
    const d = await api(`/${recurso}/${id}/playback`);

    const temCredencial = d.credential_links.length > 0;
    abrirModal(`Link de reprodução — ${titulo}`, `
      ${d.base_url_e_local ? `
        <div class="erro">
          <b>Este link só funciona dentro desta máquina.</b>
          O endereço está como <span class="mono">${esc(d.base_url)}</span>, e
          <span class="mono">localhost</span> não existe para o XC_VM nem para outro
          computador. É por isso que o link não abre fora daqui.
          <div class="grupo-botoes" style="justify-content:flex-start;margin-top:8px">
            <button class="btn btn-mini" data-ir-config>Definir o endereço correto</button>
          </div>
        </div>` : ''}

      <div>
        <div class="secao-titulo" style="margin:0 0 8px">Para testar agora (link temporário)</div>
        <p class="dica" style="margin:0 0 6px">
          Funciona por 12 horas, sem precisar de credencial. Cole no VLC em
          Mídia → Abrir Fluxo de Rede para conferir se o vídeo toca.
        </p>
        <textarea class="mono" rows="3" readonly onclick="this.select()">${esc(d.temporary_url)}</textarea>
        <div class="grupo-botoes" style="justify-content:flex-start;margin-top:6px">
          <button class="btn btn-primario btn-mini" data-copiar-temp>Copiar link temporário</button>
        </div>
      </div>

      <div>
        <div class="secao-titulo" style="margin:18px 0 8px">Para o XC_VM (link permanente)</div>
        ${temCredencial ? `
          <p class="dica" style="margin:0 0 6px">
            Já pronto, com a senha de cada cliente embutida — é só copiar e entregar. O
            link não expira e pode ser revogado a qualquer momento.
          </p>
          ${d.credential_links.map((l, i) => `
            <div style="margin-bottom:10px">
              <div class="discreto" style="margin-bottom:4px">
                ${esc(l.credential_name)}
                ${l.pronto ? '' : '<span class="etiqueta alerta">senha não recuperável</span>'}
              </div>
              <textarea class="mono" rows="3" readonly onclick="this.select()">${esc(l.url_template)}</textarea>
              <div class="grupo-botoes" style="justify-content:flex-start;margin-top:4px">
                <button class="btn btn-mini" data-copiar-link="${i}">Copiar</button>
              </div>
            </div>`).join('')}
          ${d.credential_links.some(l => !l.pronto) ? `
            <p class="dica">
              As credenciais marcadas foram criadas antes de as senhas ficarem
              recuperáveis. Use <b>Nova senha</b> em Credenciais para o link sair completo.
            </p>` : ''}
        ` : `
          <div class="erro">
            Nenhuma credencial de saída ativa. Crie uma em <b>Credenciais</b> para gerar o
            link permanente que o XC_VM vai usar.
          </div>
          <div class="grupo-botoes" style="justify-content:flex-start;margin-top:8px">
            <button class="btn btn-mini" data-ir-credenciais>Criar credencial</button>
          </div>
        `}
      </div>

      <div class="grupo-botoes">
        <button class="btn" data-acao="fechar">Fechar</button>
      </div>
    `, corpo => {
      corpo.querySelector('[data-acao=fechar]').onclick = fecharModal;
      const btnTemp = corpo.querySelector('[data-copiar-temp]');
      if (btnTemp) btnTemp.onclick = e => copiar(d.temporary_url, e.target);
      const btnCred = corpo.querySelector('[data-ir-credenciais]');
      if (btnCred) btnCred.onclick = () => { fecharModal(); irPara('#/credenciais'); };
      corpo.querySelectorAll('[data-copiar-link]').forEach(b => {
        b.onclick = e => copiar(d.credential_links[Number(b.dataset.copiarLink)].url_template, e.target);
      });
      const btnConfig = corpo.querySelector('[data-ir-config]');
      if (btnConfig) btnConfig.onclick = () => { fecharModal(); irPara('#/configuracoes'); };
    });
  } catch (err) {
    abrirModal('Link de reprodução', `
      <div class="erro">Não foi possível montar os links: ${esc(err.message)}</div>
      <div class="grupo-botoes"><button class="btn" onclick="fecharModal()">Fechar</button></div>`);
  }
}

async function abrirEpisodio(id) {
  try {
    const d = await api('/episodes/' + id);
    const e = d.episode;
    const rotulo = `${e.series_title} — T${e.season_number}E${e.episode_number}`;
    abrirModal(rotulo, `
      ${e.title ? `<h3 style="font-size:15px">${esc(e.title)}</h3>` : ''}
      ${e.plot ? `<p class="discreto">${esc(e.plot)}</p>` : ''}
      <div class="grupo-botoes" style="justify-content:flex-start">
        <button class="btn btn-primario btn-mini" data-links-ep>Link de reprodução</button>
      </div>
      <h3 style="font-size:13px;color:var(--texto-3)">Origens (${d.variants.length})</h3>
      ${d.variants.length ? tabelaVariantes(d.variants) : '<p class="discreto">Nenhuma origem.</p>'}
    `, corpo => {
      ligarAcoesVariantes();
      corpo.querySelector('[data-links-ep]').onclick = () =>
        abrirLinksReproducao('episodes', e.id, rotulo);
    });
  } catch (err) {
    aviso('Falha ao abrir episódio: ' + err.message, 'erro');
  }
}

// ---------------------------------------------------------------------------
// Tela: Categorias
// ---------------------------------------------------------------------------

// A tela de Categorias guarda o que você estava olhando.
//
// Com mais de cem categorias, cada ação recarregava a tela inteira e devolvia você ao
// topo da lista — e aí era preciso rolar e procurar de novo a linha seguinte. Unir dez
// categorias virava dez buscas manuais. O tipo escolhido, o texto do filtro e a posição da
// rolagem sobrevivem a cada ação, e é isso que transforma a tela numa fila de trabalho.
const estadoCategorias = { tipo: 'movie', busca: '' };

// recarregarCategorias redesenha sem perder o lugar.
async function recarregarCategorias() {
  const y = window.scrollY || document.documentElement.scrollTop || 0;
  await verCategorias();
  window.scrollTo(0, y);
}

async function verCategorias() {
  const [{ categories }, { pendencias }, { apelidos }] = await Promise.all([
    api('/categories'),
    api('/categorias/pendencias'),
    api('/categorias/apelidos'),
  ]);

  const tipo = estadoCategorias.tipo;
  const doTipo = lista => lista.filter(c => c.content_type === tipo);

  // As contagens das abas usam a lista INTEIRA, não a filtrada: a aba precisa dizer o que
  // existe do outro lado, senão ela não convida a ir lá.
  const contas = t => ({
    pendencias: pendencias.filter(p => p.content_type === t).length,
    categorias: categories.filter(c => c.content_type === t).length,
  });

  const principais = doTipo(categories).filter(c => c.principal);
  const outras     = doTipo(categories).filter(c => !c.principal);
  const unidas     = doTipo(apelidos);

  const aba = (valor, rotulo) => {
    const n = contas(valor);
    return `<button class="aba ${tipo === valor ? 'ativa' : ''}" data-aba="${valor}">
      ${rotulo} <span class="aba-num">${num(n.categorias)}</span>
      ${n.pendencias ? `<span class="selo">${num(n.pendencias)}</span>` : ''}
    </button>`;
  };

  // filtravel marca a linha com o texto pelo qual o campo de busca procura. Buscar no DOM
  // já desenhado, e não redesenhando a tela, é o que mantém o cursor dentro do campo
  // enquanto se digita — redesenhar a cada tecla apagaria o campo junto.
  const filtravel = nome => `data-nome="${esc(nome.toLowerCase())}"`;

  $('#visao').innerHTML = `
    <p class="discreto" style="margin:0 0 14px">
      As <b>categorias principais</b> são as pastas que os seus clientes veem. A
      sincronização não cria pasta nenhuma: ela só usa estas, e o que você vincular.
      <br><br>
      <b>Toda decisão aqui vale pelo NOME, não pela fonte.</b> Ao vincular ou unir uma
      categoria, o nome dela passa a cair naquela pasta em qualquer fonte — inclusive nas
      que você ainda vai cadastrar. É o que faz a decisão valer para sempre, e o que impede
      a categoria de voltar como pendência na próxima sincronização.
    </p>

    <div class="abas">
      ${aba('movie', 'Filmes')}
      ${aba('series', 'Séries')}
      <input class="busca-abas" id="cat-busca" placeholder="Filtrar por nome…"
             autocomplete="off" value="${esc(estadoCategorias.busca)}">
    </div>

    ${pendencias.filter(p => p.content_type === tipo).length ? `
      <div class="cartao" style="margin-top:0;border-color:#4a3a12">
        <h2>${num(pendencias.filter(p => p.content_type === tipo).length)} categoria(s) esperando decisão</h2>
        <p class="discreto" style="margin:-8px 0 14px">
          O conteúdo delas continua disponível e reproduzível — só ainda não tem pasta.
          Decidir uma resolve junto todas as outras fontes que usam o mesmo nome.
        </p>
        <div class="tabela-wrap"><table>
          <thead><tr>
            <th>Como a fonte chama</th><th>Fonte</th>
            <th>Vincular a</th><th style="width:1%"></th>
          </tr></thead>
          <tbody>${pendencias.map((p, i) => ({ p, i }))
                             .filter(({ p }) => p.content_type === tipo)
                             .map(({ p, i }) => `
            <tr ${filtravel(p.declared_name)}>
              <td><b>${esc(p.declared_name)}</b>
                  ${p.sugestao_id ? '<div class="dica">nome idêntico a uma principal</div>' : ''}</td>
              <td class="discreto">${esc(p.source_name)}</td>
              <td>
                <select data-destino="${i}" style="min-width:200px">
                  <option value="">— escolha —</option>
                  ${principais.map(c => `
                    <option value="${c.id}" ${String(p.sugestao_id) === String(c.id) ? 'selected' : ''}>
                      ${esc(c.name)}</option>`).join('')}
                </select>
              </td>
              <td><div class="grupo-botoes">
                <button class="btn btn-mini btn-primario" data-vincular="${i}">Vincular</button>
                <button class="btn btn-mini" data-promover="${i}"
                        title="Cria uma pasta nova com este nome">Criar pasta</button>
              </div></td>
            </tr>`).join('')}
          </tbody></table></div>
      </div>` : `
      <div class="cartao" style="margin-top:0">
        <div class="vazio" style="padding:20px">
          <span class="icone">✅</span>
          <h3>Nenhuma pendência ${tipo === 'movie' ? 'em filmes' : 'em séries'}</h3>
          <p>
            ${principais.length
              ? 'Toda categoria das suas fontes já tem destino definido.'
              : 'As pendências aparecem depois da próxima sincronização, quando o sistema ' +
                'lê como cada fonte nomeia suas categorias. Até lá, marque abaixo as que ' +
                'você quer manter.'}
          </p>
        </div>
      </div>`}

    <div class="secao-titulo">Categorias principais (${num(principais.length)})</div>
    ${principais.length ? `<div class="tabela-wrap"><table>
      <thead><tr>
        <th>Categoria</th><th class="numero">Conteúdos</th><th style="width:1%"></th>
      </tr></thead>
      <tbody>${principais.map(c => `
        <tr data-id="${c.id}" data-tipo="${c.content_type}" ${filtravel(c.name)}>
          <td><b class="abrir-categoria linha-clicavel">${esc(c.name)}</b></td>
          <td class="numero">${num(c.content_count)}</td>
          <td><div class="grupo-botoes">
            <button class="btn btn-mini" data-renomear="${c.id}" data-nome="${esc(c.name)}">Renomear</button>
            <button class="btn btn-mini" data-principal="${c.id}" data-valor="false">Desmarcar</button>
          </div></td>
        </tr>`).join('')}
      </tbody></table></div>`
      : `<div class="cartao"><div class="erro">
          Nenhuma categoria principal de ${tipo === 'movie' ? 'filmes' : 'séries'}. Seus
          clientes não veem pasta nenhuma até você marcar pelo menos uma abaixo, ou criar
          uma a partir de uma pendência.
        </div></div>`}

    ${outras.length ? `
      <div class="secao-titulo">Não são principais (${num(outras.length)})</div>
      <p class="discreto" style="margin:-6px 0 10px">
        Existem no catálogo mas não são destino de nada — os seus clientes não veem estas
        pastas. Há duas saídas: <b>tornar principal</b>, se a pasta deve aparecer como
        está; ou <b>unir</b> a uma principal, se o conteúdo dela pertence a outra pasta.
        Unir move o conteúdo, apaga a pasta antiga e <b>guarda o nome</b> na lista de
        categorias unidas, lá embaixo — de onde dá para voltar atrás.
      </p>
      <div class="tabela-wrap"><table>
        <thead><tr>
          <th>Categoria</th><th class="numero">Conteúdos</th><th style="width:1%"></th>
        </tr></thead>
        <tbody>${outras.map(c => `
          <tr data-id="${c.id}" data-tipo="${c.content_type}" ${filtravel(c.name)}>
            <td>${esc(c.name)}</td>
            <td class="numero">${num(c.content_count)}</td>
            <td><div class="grupo-botoes">
              <button class="btn btn-mini btn-primario" data-principal="${c.id}"
                      data-valor="true">Tornar principal</button>
              ${principais.length ? `
                <select data-uniao="${c.id}" style="min-width:170px">
                  <option value="">— unir a… —</option>
                  ${principais.map(pr =>
                    `<option value="${pr.id}">${esc(pr.name)}</option>`).join('')}
                </select>
                <button class="btn btn-mini" data-unir-cat="${c.id}"
                        data-nome="${esc(c.name)}" data-itens="${c.content_count}">Unir</button>` : ''}
            </div></td>
          </tr>`).join('')}
        </tbody></table></div>` : ''}

    ${unidas.length ? `
      <div class="secao-titulo">Categorias unidas (${num(unidas.length)})</div>
      <p class="discreto" style="margin:-6px 0 10px">
        Nomes que caem sempre na mesma pasta, <b>em qualquer fonte</b>. Enquanto o nome
        estiver nesta lista, ele nunca mais volta a pedir decisão — é o que impede a
        categoria de ressurgir a cada sincronização.
        <br>
        <b>Soltar</b> devolve o nome à fila de pendências, para você decidir de novo.
        <b>Reativar</b> recria a pasta como principal e traz de volta o conteúdo que veio
        dela.
      </p>
      <div class="tabela-wrap"><table>
        <thead><tr>
          <th>Nome da categoria</th><th>Cai em</th><th class="numero">Fontes</th>
          <th>Desde</th><th style="width:1%"></th>
        </tr></thead>
        <tbody>${unidas.map(a => `
          <tr ${filtravel(a.declared_name + ' ' + a.category_name)}>
            <td><b>${esc(a.declared_name)}</b>
                ${a.origem === 'uniao'
                  ? '<div class="dica">pasta unida</div>'
                  : '<div class="dica">pendência vinculada</div>'}</td>
            <td>${esc(a.category_name)}</td>
            <td class="numero">${num(a.fontes)}</td>
            <td class="discreto">${esc(a.created_at)}</td>
            <td><div class="grupo-botoes">
              <button class="btn btn-mini" data-apelido-reativar="${a.id}"
                      data-nome="${esc(a.declared_name)}"
                      data-destino="${esc(a.category_name)}">Reativar</button>
              <button class="btn btn-mini" data-apelido-soltar="${a.id}"
                      data-nome="${esc(a.declared_name)}">Soltar</button>
            </div></td>
          </tr>`).join('')}
        </tbody></table></div>` : ''}
  `;

  // Abas.
  $$('[data-aba]').forEach(b => {
    b.onclick = () => {
      estadoCategorias.tipo = b.dataset.aba;
      window.scrollTo(0, 0);
      verCategorias();
    };
  });

  // Filtro por nome, aplicado sobre o que já está desenhado.
  const busca = $('#cat-busca');
  const aplicarFiltro = () => {
    const termo = busca.value.trim().toLowerCase();
    estadoCategorias.busca = busca.value;
    $$('tr[data-nome]').forEach(tr => {
      tr.hidden = termo !== '' && !tr.dataset.nome.includes(termo);
    });
  };
  busca.oninput = aplicarFiltro;
  if (estadoCategorias.busca) aplicarFiltro();

  // Marcar e desmarcar principal.
  $$('[data-principal]').forEach(b => {
    b.onclick = () => comAcao(async () => {
      try {
        await api(`/categories/${b.dataset.principal}/principal`, {
          method: 'PUT',
          corpo: { principal: b.dataset.valor === 'true' },
        });
        recarregarCategorias();
      } catch (err) { aviso('Falha: ' + err.message, 'erro'); }
    });
  });

  // Resolver pendências.
  const resolver = async (p, corpo, botao) => {
    botao.disabled = true;
    try {
      const r = await api(`/categorias/pendencias/${p.id}/resolver`, { method: 'POST', corpo });
      const movidos = r.conteudos_movidos || 0;
      const outrasFontes = r.outras_fontes || 0;
      const partes = ['Vinculado.'];
      if (movidos > 0) partes.push(`${num(movidos)} conteúdo(s) movido(s).`);
      if (outrasFontes > 0) partes.push(`Resolveu junto ${num(outrasFontes)} categoria(s) de outras fontes.`);
      aviso(partes.join(' '), 'ok');
      recarregarCategorias();
    } catch (err) {
      aviso('Falha: ' + err.message, 'erro');
      botao.disabled = false;
    }
  };

  $$('[data-vincular]').forEach(b => {
    b.onclick = () => comAcao(async () => {
      const i = Number(b.dataset.vincular);
      const destino = $(`[data-destino="${i}"]`).value;
      if (!destino) {
        aviso('Escolha uma categoria, ou use "Criar pasta".', 'erro');
        return;
      }
      await resolver(pendencias[i], { categoria_id: Number(destino) }, b);
    });
  });

  $$('[data-promover]').forEach(b => {
    b.onclick = () => comAcao(async () => {
      const p = pendencias[Number(b.dataset.promover)];
      const nome = await perguntar('Criar categoria principal',
        'Nome da pasta que os seus clientes vão ver:', p.declared_name);
      if (nome === null) return;
      await resolver(p, { promover: true, nome }, b);
    });
  });

  // Unir uma categoria a uma principal.
  //
  // Apaga a categoria de origem, então confirma antes — e diz quantos conteúdos vão se
  // mover, que é o número que faz a pessoa perceber se escolheu a linha errada.
  $$('[data-unir-cat]').forEach(b => {
    b.onclick = () => comAcao(async () => {
      const origem = b.dataset.unirCat;
      const seletor = $(`[data-uniao="${origem}"]`);
      const destino = seletor ? seletor.value : '';
      if (!destino) {
        aviso('Escolha ao lado a categoria principal que vai receber o conteúdo.', 'erro');
        return;
      }
      const nomeDestino = seletor.options[seletor.selectedIndex].textContent.trim();
      const itens = Number(b.dataset.itens) || 0;
      const ok = await confirmar('Unir categoria',
        `Mover ${num(itens)} conteúdo(s) de "${b.dataset.nome}" para "${nomeDestino}" ` +
        `e apagar a pasta "${b.dataset.nome}"? O conteúdo não é apagado, e o nome fica ` +
        'guardado em "Categorias unidas" — dá para voltar atrás.');
      if (!ok) return;

      b.disabled = true;
      try {
        const r = await api(`/categories/${origem}/absorver`, {
          method: 'POST',
          corpo: { categoria_id: Number(destino) },
        });
        aviso(`Unido. ${num(r.conteudos_movidos || 0)} conteúdo(s) movido(s). ` +
              `"${b.dataset.nome}" cai em "${nomeDestino}" daqui em diante.`, 'ok');
        recarregarCategorias();
      } catch (err) {
        aviso('Falha: ' + err.message, 'erro');
        b.disabled = false;
      }
    });
  });

  // Soltar um nome: ele volta a pedir decisão.
  $$('[data-apelido-soltar]').forEach(b => {
    b.onclick = () => comAcao(async () => {
      const ok = await confirmar('Soltar o nome',
        `"${b.dataset.nome}" volta a aparecer como pendência na próxima sincronização, ` +
        'para você decidir de novo. O conteúdo que já foi movido continua onde está.',
        'Soltar');
      if (!ok) return;
      b.disabled = true;
      try {
        await api(`/categorias/apelidos/${b.dataset.apelidoSoltar}`, { method: 'DELETE' });
        aviso(`"${b.dataset.nome}" voltou a pedir decisão.`, 'ok');
        recarregarCategorias();
      } catch (err) {
        aviso('Falha: ' + err.message, 'erro');
        b.disabled = false;
      }
    });
  });

  // Reativar: a pasta volta a existir, principal, com o conteúdo que veio dela.
  $$('[data-apelido-reativar]').forEach(b => {
    b.onclick = () => comAcao(async () => {
      const ok = await confirmar('Reativar como categoria principal',
        `"${b.dataset.nome}" volta a ser uma pasta própria e marcada como principal. ` +
        `O conteúdo que veio dela sai de "${b.dataset.destino}" e volta para ela. ` +
        'Itens que a fonte parou de declarar nesse nome ficam onde estão.',
        'Reativar');
      if (!ok) return;
      b.disabled = true;
      try {
        const r = await api(`/categorias/apelidos/${b.dataset.apelidoReativar}/reativar`,
          { method: 'POST' });
        aviso(`"${b.dataset.nome}" voltou como categoria principal. ` +
              `${num(r.conteudos_movidos || 0)} conteúdo(s) de volta.`, 'ok');
        recarregarCategorias();
      } catch (err) {
        aviso('Falha: ' + err.message, 'erro');
        b.disabled = false;
      }
    });
  });

  ligarAcoesCategorias(categories);
}

function tabelaMapeamento(itens) {
  return `<div class="tabela-wrap"><table>
    <thead><tr>
      <th>Fonte</th><th>Nome declarado</th><th class="numero">Itens</th>
      <th>Categoria no catálogo</th><th style="width:1%"></th>
    </tr></thead>
    <tbody>${itens.map(sc => `
      <tr>
        <td class="discreto">${esc(sc.source_name)}</td>
        <td><b>${esc(sc.declared_name)}</b></td>
        <td class="numero">${num(sc.item_count)}</td>
        <td>${sc.category_name
              ? esc(sc.category_name)
              : '<span class="etiqueta alerta">sem vínculo</span>'}
            ${sc.suggestions.length ? `<div class="discreto" style="margin-top:3px">
               parecida com: ${sc.suggestions.slice(0, 2).map(s =>
                 `${esc(s.name)} (${Math.round(s.similarity * 100)}%)`).join(', ')}</div>` : ''}</td>
        <td><button class="btn btn-mini" data-mapear="${sc.id}">Unificar…</button></td>
      </tr>`).join('')}
    </tbody></table></div>`;
}

function ligarAcoesCategorias(categorias) {
  $$('.abrir-categoria').forEach(el => {
    el.onclick = () => {
      const tr = el.closest('tr');
      const tipo = tr.dataset.tipo === 'movie' ? 'movie' : 'series';
      filtroCatalogo[tipo].categoria = tr.dataset.id;
      filtroCatalogo[tipo].offset = 0;
      irPara(tipo === 'movie' ? '#/filmes' : '#/series');
    };
  });

  $$('[data-renomear]').forEach(botao => {
    botao.onclick = () => {
      const id = botao.dataset.renomear;
      abrirModal('Renomear categoria', `
        <label>Nome exibido <input id="cat-nome" value="${esc(botao.dataset.nome)}"></label>
        <p class="dica">Só muda o nome exibido. O agrupamento continua o mesmo.</p>
        <div class="grupo-botoes">
          <button class="btn" data-acao="cancelar">Cancelar</button>
          <button class="btn btn-primario" data-acao="salvar">Salvar</button>
        </div>
      `, corpo => {
        corpo.querySelector('[data-acao=cancelar]').onclick = fecharModal;
        corpo.querySelector('[data-acao=salvar]').onclick = async () => {
          try {
            await api(`/categories/${id}`, {
              method: 'PATCH', corpo: { name: corpo.querySelector('#cat-nome').value },
            });
            fecharModal(); aviso('Categoria renomeada.', 'ok'); navegar();
          } catch (err) { aviso('Falha: ' + err.message, 'erro'); }
        };
      });
    };
  });

  $$('[data-mapear]').forEach(botao => {
    botao.onclick = async () => {
      const { source_categories } = await api('/source-categories');
      const sc = source_categories.find(x => String(x.id) === botao.dataset.mapear);
      if (sc) formularioUnificarCategoria(sc, categorias);
    };
  });
}

function formularioUnificarCategoria(sc, categorias) {
  const doTipo = categorias.filter(c =>
    c.content_type === sc.content_type || sc.content_type === 'unknown');

  abrirModal(`Unificar "${sc.declared_name}"`, `
    <p class="discreto">
      Da fonte <b>${esc(sc.source_name)}</b> — ${num(sc.item_count)} itens.
      Escolher uma categoria move esses itens para ela no catálogo.
    </p>

    ${sc.suggestions.length ? `
      <div>
        <div class="secao-titulo" style="margin:0 0 8px">Sugestões</div>
        ${sc.suggestions.map(s => `
          <button class="btn btn-largo" style="justify-content:space-between;text-align:left;margin-bottom:6px"
                  data-sugestao="${s.category_id}">
            ${esc(s.name)}
            <span class="discreto">${Math.round(s.similarity * 100)}% parecida · ${num(s.item_count)} itens</span>
          </button>`).join('')}
      </div>` : ''}

    <label>Ou escolha uma categoria existente
      <select id="uc-existente">
        <option value="">— selecione —</option>
        ${doTipo.map(c => `<option value="${c.id}" ${String(sc.category_id) === String(c.id) ? 'selected' : ''}>
            ${esc(c.name)} (${num(c.content_count)})</option>`).join('')}
      </select>
    </label>

    <label>Ou crie uma nova
      <input id="uc-nova" placeholder="Ex.: Ação">
    </label>

    <div class="erro" id="uc-erro" hidden></div>
    <div class="grupo-botoes">
      <button class="btn" data-acao="cancelar">Cancelar</button>
      <button class="btn btn-primario" data-acao="aplicar">Aplicar</button>
    </div>
  `, corpo => {
    corpo.querySelector('[data-acao=cancelar]').onclick = fecharModal;

    const aplicar = async payload => {
      const erro = corpo.querySelector('#uc-erro');
      erro.hidden = true;
      try {
        const r = await api(`/source-categories/${sc.id}/map`, { method: 'POST', corpo: payload });
        fecharModal();
        aviso(`Unificada. ${num(r.contents_moved)} conteúdo(s) movido(s).`, 'ok');
        navegar();
      } catch (err) {
        erro.textContent = err.message;
        erro.hidden = false;
      }
    };

    corpo.querySelectorAll('[data-sugestao]').forEach(b => {
      b.onclick = () => aplicar({ category_id: Number(b.dataset.sugestao) });
    });

    corpo.querySelector('[data-acao=aplicar]').onclick = () => {
      const nova = corpo.querySelector('#uc-nova').value.trim();
      const existente = corpo.querySelector('#uc-existente').value;
      if (nova) return aplicar({ new_name: nova });
      if (existente) return aplicar({ category_id: Number(existente) });
      const erro = corpo.querySelector('#uc-erro');
      erro.textContent = 'Escolha uma sugestão, uma categoria existente, ou digite o nome de uma nova.';
      erro.hidden = false;
    };
  });
}

// ---------------------------------------------------------------------------
// Tela: Não resolvidos
// ---------------------------------------------------------------------------

const motivos = {
  sem_titulo: 'A fonte não informou título',
  sem_midia: 'O item não aponta para nenhuma mídia',
  nao_e_vod: 'É transmissão ao vivo, não VOD',
  tipo_indeterminado: 'Não deu para decidir entre filme e episódio',
  categoria_filtrada: 'Excluído pelo filtro de categorias da fonte',
  temporada_episodio_ausente: 'Parece série, mas sem temporada/episódio',
  url_invalida: 'A URL não é http(s) válida',
};

async function verNaoResolvidos() {
  const { items } = await api('/unresolved?limit=200');
  if (!items.length) {
    $('#visao').innerHTML = `<div class="cartao"><div class="vazio">
      <span class="icone">✅</span><h3>Nada pendente</h3>
      <p>Todos os itens das suas fontes foram classificados como filme ou episódio.</p>
    </div></div>`;
    return;
  }
  $('#visao').innerHTML = `
    <p class="discreto" style="margin:0 0 14px">
      Itens que o sistema não conseguiu classificar com segurança. Eles não entram no catálogo,
      mas também não são descartados em silêncio — um item de série sem numeração nunca vira
      filme por descarte.
    </p>
    <div class="tabela-wrap"><table>
      <thead><tr><th>Título declarado</th><th>Fonte</th><th>Grupo</th><th>Motivo</th><th class="numero">Vezes</th></tr></thead>
      <tbody>${items.map(i => `
        <tr>
          <td><b>${esc(i.declared_title) || '<span class="discreto">sem título</span>'}</b></td>
          <td class="discreto">${esc(i.source_name)}</td>
          <td class="discreto">${esc(i.declared_group) || '—'}</td>
          <td><span class="etiqueta alerta">${esc(motivos[i.reason] || i.reason)}</span></td>
          <td class="numero">${num(i.occurrences)}</td>
        </tr>`).join('')}
      </tbody></table></div>`;
}

// mostrarLinksDaLista entrega os endereços prontos de uma credencial.
//
// Vêm do servidor já com a senha embutida: nada para o administrador substituir à mão, e
// nada para o cliente digitar. É só copiar e entregar.
async function mostrarLinksDaLista(c) {
  const d = await api(`/stream-credentials/${c.id}/links`);

  if (!d.senha_disponivel) {
    abrirModal(`Lista de ${c.name}`, `
      <div class="erro">
        <b>Esta credencial foi criada antes de as senhas ficarem recuperáveis.</b>
        Por isso o sistema não consegue montar os endereços prontos dela.
      </div>
      <p class="discreto">
        Use <b>Nova senha</b>: o usuário e os links continuam os mesmos, e a partir daí os
        endereços aparecem completos aqui. Quem estiver assistindo é desconectado.
      </p>
      <div class="grupo-botoes">
        <button class="btn" data-acao=fechar>Fechar</button>
      </div>
    `, corpo => { corpo.querySelector('[data-acao=fechar]').onclick = fecharModal; });
    return;
  }

  const linha = (titulo, dica, valor, indice) => `
    <div class="secao-titulo" style="margin:0 0 6px">${titulo}</div>
    <p class="dica" style="margin:-2px 0 6px">${dica}</p>
    <textarea class="mono" rows="3" readonly onclick="this.select()">${esc(valor)}</textarea>
    <div class="grupo-botoes" style="justify-content:flex-start;margin:6px 0 16px">
      <button class="btn btn-mini" data-copiar="${indice}">Copiar</button>
    </div>`;

  const valores = [d.m3u_url, d.base_url, d.username, d.password, d.m3u_filmes_url, d.m3u_series_url];

  abrirModal(`Acesso de ${c.name}`, `
    ${d.base_url_e_local ? `
      <div class="erro">
        <b>Estes endereços só funcionam dentro desta máquina.</b>
        O endereço está como <span class="mono">${esc(d.base_url)}</span>.
        <div class="grupo-botoes" style="justify-content:flex-start;margin-top:8px">
          <button class="btn btn-mini" data-ir-config>Definir o endereço correto</button>
        </div>
      </div>` : ''}
    ${!d.ativa ? `
      <div class="erro">
        Esta credencial está <b>revogada, desativada ou expirada</b>. Os endereços abaixo
        só voltam a funcionar quando ela for reativada.
      </div>` : ''}

    ${linha('Lista M3U — catálogo completo',
            'Pronta para usar. Cole em qualquer aplicativo que aceite lista M3U.',
            d.m3u_url, 0)}

    <div class="secao-titulo" style="margin:0 0 6px">Xtream Codes — para o XC_VM</div>
    <p class="dica" style="margin:-2px 0 6px">
      Cadastre como servidor Xtream. É o formato que traz pastas, capas e sinopses.
    </p>
    <table class="tabela-simples">
      <tr><td class="discreto" style="width:90px">Servidor</td><td class="mono">${esc(d.base_url)}</td></tr>
      <tr><td class="discreto">Usuário</td><td class="mono">${esc(d.username)}</td></tr>
      <tr><td class="discreto">Senha</td><td class="mono">${esc(d.password)}</td></tr>
    </table>
    <div class="grupo-botoes" style="justify-content:flex-start;margin:10px 0 16px">
      <button class="btn btn-mini" data-copiar="1">Copiar servidor</button>
      <button class="btn btn-mini" data-copiar="2">Copiar usuário</button>
      <button class="btn btn-mini" data-copiar="3">Copiar senha</button>
    </div>

    ${linha('Só filmes', 'Quando o cliente não deve receber as séries.', d.m3u_filmes_url, 4)}
    ${linha('Só séries', 'Quando o cliente não deve receber os filmes.', d.m3u_series_url, 5)}

    <div class="grupo-botoes"><button class="btn" data-acao=fechar>Fechar</button></div>
  `, corpo => {
    corpo.querySelector('[data-acao=fechar]').onclick = fecharModal;
    corpo.querySelectorAll('[data-copiar]').forEach(b => {
      b.onclick = e => copiar(valores[Number(b.dataset.copiar)], e.target);
    });
    const btnConfig = corpo.querySelector('[data-ir-config]');
    if (btnConfig) btnConfig.onclick = () => { fecharModal(); irPara('#/configuracoes'); };
  });
}

// ---------------------------------------------------------------------------
// Tela: Duplicatas sugeridas
// ---------------------------------------------------------------------------

async function verDuplicatas() {
  const d = await api('/duplicatas');

  const cartao = (c, outro, i, lado) => `
    <div class="lado-duplicata">
      ${c.poster_url
        ? `<img class="cartaz" src="${esc(c.poster_url)}" alt="" loading="lazy"
             onerror="this.style.display='none'">`
        : '<div class="sem-cartaz">🎬</div>'}
      <div style="flex:1;min-width:0">
        <b>${esc(c.titulo)}</b>
        ${c.tem_marcacao ? '<span class="etiqueta alerta">marcação</span>' : ''}
        <div class="dica">
          ${c.ano ?? 'sem ano'} ·
          ${num(c.variantes)} fonte${c.variantes === 1 ? '' : 's'} ·
          <span class="mono">id ${c.id}</span>
        </div>
        <button class="btn btn-mini btn-primario" data-unir="${i}" data-manter="${c.id}">
          Manter este
        </button>
      </div>
    </div>`;

  $('#visao').innerHTML = `
    <p class="discreto" style="margin:0 0 14px">
      Pares que <b>parecem</b> ser o mesmo conteúdo: mesmo tipo, mesmo ano e mesmo título
      depois de ignorar a palavra <b>"Lançamento"</b>. O sistema aponta;
      <b>quem decide é você</b>.
      <br><br>
      Nada é agrupado sozinho. Uma regra que remove "Lançamento" acerta quase sempre e
      erraria em silêncio no dia em que aparecesse um filme com esse nome de verdade.
    </p>

    ${d.limitado ? `
      <div class="veredito alerta">
        Mostrando os primeiros ${num(d.total)} pares. Resolva estes e a lista traz os
        próximos.
      </div>` : ''}

    ${d.sugestoes.length ? d.sugestoes.map((s, i) => `
      <div class="cartao par-duplicata" data-par="${i}">
        <div class="duplicata-lados">
          ${cartao(s.a, s.b, i, 'a')}
          <div class="duplicata-versus">ou</div>
          ${cartao(s.b, s.a, i, 'b')}
        </div>
        <div class="grupo-botoes" style="justify-content:flex-start;margin-top:10px">
          <button class="btn btn-mini" data-diferentes="${i}">São conteúdos diferentes</button>
        </div>
      </div>`).join('')
      : `<div class="cartao"><div class="vazio">
          <span class="icone">✅</span>
          <h3>Nenhuma duplicata sugerida</h3>
          <p>Não há pares parecidos esperando decisão.</p>
        </div></div>`}
  `;

  const decidir = async (i, manter, botao) => {
    const s = d.sugestoes[i];
    botao.disabled = true;
    try {
      const r = await api('/duplicatas/decidir', {
        method: 'POST',
        corpo: { a: s.a.id, b: s.b.id, manter },
      });
      if (r.unidos) {
        aviso(`Unidos. ${num(r.variantes_movidas)} fonte(s) movida(s). ` +
              'Quem já importou o item removido precisa reimportar.', 'ok');
      } else {
        aviso('Marcados como conteúdos diferentes.', 'ok');
      }
      const linha = $(`[data-par="${i}"]`);
      if (linha) linha.remove();
    } catch (err) {
      aviso('Falha: ' + err.message, 'erro');
      botao.disabled = false;
    }
  };

  $$('[data-unir]').forEach(b => {
    b.onclick = () => comAcao(async () => {
      const i = Number(b.dataset.unir);
      const s = d.sugestoes[i];
      const manter = Number(b.dataset.manter);
      const remover = manter === s.a.id ? s.b : s.a;

      const ok = await confirmar('Unir conteúdos',
        `As fontes de "${remover.titulo}" passam para "${manter === s.a.id ? s.a.titulo : s.b.titulo}", ` +
        `e o item removido deixa de existir. ` +
        `Quem já importou o id ${remover.id} vai precisar reimportar para vê-lo de novo.`,
        'Unir');
      if (!ok) return;
      await decidir(i, manter, b);
    });
  });

  $$('[data-diferentes]').forEach(b => {
    b.onclick = () => comAcao(async () => {
      await decidir(Number(b.dataset.diferentes), 0, b);
    });
  });
}

// ---------------------------------------------------------------------------
// Tela: Falhas de reprodução
// ---------------------------------------------------------------------------

// CAUSAS descreve cada código de falha e, sobretudo, DE QUEM é o problema.
//
// A segunda parte é a que faltava. Sem ela, toda falha parece igualmente nossa, e a reação
// natural é procurar defeito no sistema — inclusive quando a fonte é que cortou a entrega, e
// não há linha de código que conserte isso.
//
// A coluna `culpa` distingue três coisas que pedem providências opostas: `fonte` (o
// fornecedor entregou mal), `nosso` (defeito ou limite desta instalação) e `cliente` (nada a
// fazer, é comportamento de quem assiste).
const CAUSAS = {
  todas_as_origens_falharam: {
    culpa: 'fonte', rotulo: 'Nenhuma fonte respondeu',
    detalhe: 'Todas as origens deste conteúdo falharam. Costuma ser link morto ou credencial vencida.',
  },
  origem_indisponivel: {
    culpa: 'fonte', rotulo: 'A fonte recusou ou não respondeu',
    detalhe: 'A fonte não aceitou a conexão. Credencial, limite de conexões, ou fora do ar.',
  },
  fonte_nao_enviou_dados: {
    culpa: 'fonte', rotulo: 'A fonte aceitou e não enviou nada',
    detalhe: 'A conexão abriu e nenhum byte veio. Quase sempre é cadastro: credencial ou link errado.',
  },
  fonte_parou_no_meio: {
    culpa: 'fonte', rotulo: 'A fonte parou de enviar no meio',
    detalhe: 'Começou bem e travou. É a fonte engasgando sob carga — aparece em rajadas no mesmo horário.',
  },
  fonte_entregou_menos: {
    culpa: 'fonte', rotulo: 'A fonte cortou antes do fim',
    detalhe: 'Ela encerrou antes de entregar o tamanho que anunciou. O filme para no meio e o player volta ao começo. É a falha mais comum de fonte sob carga — e o cache é o que a elimina, porque um arquivo guardado não pode ser cortado.',
  },
  video_de_manutencao: {
    culpa: 'fonte', rotulo: 'A fonte devolveu vídeo de manutenção',
    detalhe: 'Veio um arquivo curto no lugar do filme. O sistema detectou e tentou a próxima origem.',
  },
  falha_no_acervo: {
    culpa: 'nosso', rotulo: 'Falha ao ler o arquivo guardado',
    detalhe: 'Disco com problema, ou conta de nuvem fora. A reprodução caiu de volta para a fonte.',
  },
  falha_na_copia: {
    culpa: 'nosso', rotulo: 'A transmissão foi interrompida',
    detalhe: 'Erro não classificado durante a entrega. Vale olhar o registro do serviço.',
  },
  sessao_abandonada: {
    culpa: 'nosso', rotulo: 'Sessão ficou aberta tempo demais',
    detalhe: 'Encerrada pela faxina. Ocupava vaga na credencial sem estar entregando nada.',
  },
  processo_encerrado: {
    culpa: 'nosso', rotulo: 'O serviço reiniciou durante a reprodução',
    detalhe: 'Esperado logo após uma atualização. Em outro momento, investigue por que ele caiu.',
  },
  cliente_desconectou: {
    culpa: 'cliente', rotulo: 'O espectador fechou o player',
    detalhe: 'Comportamento normal de quem assiste. Não é falha.',
  },
};

async function verFalhas() {
  const { falhas, resumo } = await api('/falhas');

  const explicar = c => (CAUSAS[c] || {}).rotulo || c || 'erro não identificado';

  $('#visao').innerHTML = `
    <p class="discreto" style="margin:0 0 14px">
      Reproduções que <b>não entregaram o vídeo</b>. Cada linha diz o que o cliente tentou
      abrir e de qual fonte o sistema tentou puxar — é a fonte que aponta o que fazer.
      <br><br>
      Player fechado no meio do filme não aparece aqui: é o comportamento normal de quem
      assiste, e listá-lo afogaria os problemas de verdade.
    </p>

    ${resumoDeFalhas(resumo)}

    ${falhas.length ? `
      <div class="tabela-wrap"><table>
        <thead><tr>
          <th>Quando</th><th>Conteúdo</th><th>Fonte</th><th>Motivo</th>
          <th class="numero">Tentativas</th><th>Cliente</th>
        </tr></thead>
        <tbody>${falhas.map(f => `
          <tr${f.content_id ? ' class="linha-clicavel" data-conteudo="' + f.content_id + '"' : ''}>
            <td class="discreto">${tempoRelativo(f.started_at)}</td>
            <td><b>${esc(f.titulo)}</b>
                ${f.episode_id ? '<span class="etiqueta neutro">episódio</span>' : ''}</td>
            <td>${f.source_name
                  ? esc(f.source_name)
                  : '<span class="discreto">nenhuma respondeu</span>'}</td>
            <td><span class="etiqueta erro">${esc(explicar(f.error_code))}</span>
                ${f.status_code ? `<span class="discreto"> HTTP ${f.status_code}</span>` : ''}</td>
            <td class="numero">${num(f.tentativas)}</td>
            <td class="discreto">${esc(f.credencial || '—')}<br>
                <span class="mono" style="font-size:11px">${esc(f.client_ip)}</span></td>
          </tr>`).join('')}
        </tbody></table></div>`
      : `<div class="cartao"><div class="vazio">
          <span class="icone">✅</span>
          <h3>Nenhuma falha registrada</h3>
          <p>Toda reprodução recente entregou o vídeo.</p>
        </div></div>`}
  `;

  $$('#visao tr[data-conteudo]').forEach(tr => {
    tr.onclick = () => irPara('#/conteudo/' + tr.dataset.conteudo);
  });
}

// ---------------------------------------------------------------------------
// Tela: Usuários do painel
// ---------------------------------------------------------------------------

async function verUsuarios() {
  let d;
  try {
    d = await api('/users');
  } catch (err) {
    $('#visao').innerHTML = `<div class="cartao"><div class="erro">
      ${esc(err.message)}<br><br>Só administradores gerenciam usuários.</div></div>`;
    return;
  }

  $('#acoes-pagina').innerHTML =
    '<button class="btn btn-primario" id="novo-usuario">+ Novo usuário</button>';
  $('#novo-usuario').onclick = () => formularioUsuario(d.papeis, d.descricao);

  $('#visao').innerHTML = `
    <p class="discreto" style="margin:0 0 14px">
      Quem pode entrar no painel. Cada pessoa com a própria conta: assim você vê quem fez
      o quê nos <b>Eventos</b>, e corta o acesso de alguém sem trocar a senha de todos.
    </p>

    <div class="tabela-wrap"><table>
      <thead><tr>
        <th>Usuário</th><th>Papel</th><th>Estado</th><th>Último acesso</th>
        <th style="width:1%"></th>
      </tr></thead>
      <tbody>${d.users.map(u => `
        <tr>
          <td><b>${esc(u.username)}</b>
              ${u.id === d.eu ? '<span class="etiqueta ok">você</span>' : ''}</td>
          <td>${esc(u.role)}<div class="dica">${esc(d.descricao[u.role] || '')}</div></td>
          <td>${u.enabled
                ? '<span class="etiqueta ok">ativo</span>'
                : '<span class="etiqueta neutro">desativado</span>'}</td>
          <td class="discreto">${tempoRelativo(u.last_login_at)}</td>
          <td><div class="grupo-botoes">
            <button class="btn btn-mini" data-editar-usuario="${u.id}">Editar</button>
            ${u.id === d.eu ? '' :
              `<button class="btn btn-mini btn-perigo" data-excluir-usuario="${u.id}"
                       data-nome="${esc(u.username)}">Excluir</button>`}
          </div></td>
        </tr>`).join('')}
      </tbody></table></div>
  `;

  $$('[data-editar-usuario]').forEach(b => {
    const u = d.users.find(x => String(x.id) === b.dataset.editarUsuario);
    b.onclick = () => formularioUsuario(d.papeis, d.descricao, u);
  });

  $$('[data-excluir-usuario]').forEach(b => {
    b.onclick = () => comAcao(async () => {
      const ok = await confirmar('Remover usuário',
        `Remover "${b.dataset.nome}"? A pessoa perde o acesso imediatamente.`, 'Remover');
      if (!ok) return;
      try {
        await api('/users/' + b.dataset.excluirUsuario, { method: 'DELETE' });
        aviso('Usuário removido.', 'ok');
        navegar();
      } catch (err) { aviso('Falha: ' + err.message, 'erro'); }
    });
  });
}

function formularioUsuario(papeis, descricao, u) {
  const editando = !!u;
  abrirModal(editando ? `Editar ${u.username}` : 'Novo usuário', `
    ${editando ? '' : `
      <label>Usuário <input id="us-nome" autocomplete="off" placeholder="Ex.: socio"></label>`}
    <label>Papel
      <select id="us-papel">
        ${papeis.map(p => `<option value="${p}" ${editando && u.role === p ? 'selected' : ''}>${p}</option>`).join('')}
      </select>
    </label>
    <p class="dica" id="us-descricao"></p>
    <label>${editando ? 'Nova senha (em branco = manter a atual)' : 'Senha'}
      <input type="password" id="us-senha" autocomplete="new-password">
    </label>
    <p class="dica">Ao menos 12 caracteres.</p>
    ${editando ? `
      <label class="linha-check">
        <input type="checkbox" id="us-ativo" ${u.enabled ? 'checked' : ''}> Conta ativa
      </label>` : ''}
    <div class="erro" id="us-erro" hidden></div>
    <div class="grupo-botoes">
      <button class="btn" data-acao="cancelar">Cancelar</button>
      <button class="btn btn-primario" data-acao="salvar">Salvar</button>
    </div>
  `, corpo => {
    const seletor = corpo.querySelector('#us-papel');
    const mostrar = () => {
      corpo.querySelector('#us-descricao').textContent = descricao[seletor.value] || '';
    };
    seletor.onchange = mostrar;
    mostrar();

    corpo.querySelector('[data-acao=cancelar]').onclick = fecharModal;
    corpo.querySelector('[data-acao=salvar]').onclick = async e => {
      const erro = corpo.querySelector('#us-erro');
      erro.hidden = true;
      e.target.disabled = true;
      const senha = corpo.querySelector('#us-senha').value;
      try {
        if (editando) {
          const dados = {
            role: seletor.value,
            enabled: corpo.querySelector('#us-ativo').checked,
          };
          if (senha) dados.password = senha;
          await api('/users/' + u.id, { method: 'PATCH', corpo: dados });
        } else {
          await api('/users', {
            method: 'POST',
            corpo: {
              username: corpo.querySelector('#us-nome').value.trim(),
              password: senha,
              role: seletor.value,
            },
          });
        }
        fecharModal();
        aviso('Usuário salvo.', 'ok');
        navegar();
      } catch (err) {
        erro.textContent = err.message;
        erro.hidden = false;
        e.target.disabled = false;
      }
    };
  });
}

// ---------------------------------------------------------------------------
// Tela: Sistema (recursos da máquina)
// ---------------------------------------------------------------------------


async function verSistema() {
  const d = await api('/system');
  const a = d.amostra, v = d.veredito;

  const cor = { ok: 'ok', atencao: 'alerta', critico: 'erro', desconhecido: 'neutro' };

  const barra = (pct, nivel) => pct == null ? '' : `
    <div class="barra-uso"><div class="barra-uso-preenchida ${cor[nivel] || ''}"
         style="width:${Math.min(100, Math.max(0, pct))}%"></div></div>`;

  $('#visao').innerHTML = `
    <div class="cartao" style="margin-top:0">
      <div class="veredito ${cor[v.nivel] || ''}">
        <b>${esc(v.titulo)}</b>
      </div>
      ${v.pontos.length ? `
        <table class="tabela-simples" style="margin-top:12px">
          ${v.pontos.map(p => `
            <tr>
              <td style="width:110px;vertical-align:top">
                <b>${esc(p.recurso)}</b>
                <div><span class="etiqueta ${cor[p.nivel] || 'neutro'}">${esc(p.nivel)}</span></div>
              </td>
              <td>
                ${esc(p.situacao)}
                ${p.percentual ? barra(p.percentual, p.nivel) : ''}
                ${p.sugestao ? `<div class="dica" style="margin-top:4px">${esc(p.sugestao)}</div>` : ''}
              </td>
            </tr>`).join('')}
        </table>` : ''}
    </div>

    ${a.disponivel ? `
      <div class="cartao">
        <h2>Números medidos</h2>
        <table class="tabela-simples">
          <tr><td class="discreto" style="width:180px">Processador</td>
              <td>${a.cpu_percent < 0 ? '<span class="discreto">medindo…</span>'
                    : `${a.cpu_percent}% de ${a.cpus} núcleo${a.cpus > 1 ? 's' : ''}`}
                  ${a.load_average ? `<span class="discreto"> · carga ${a.load_average.map(x => x.toFixed(2)).join(' / ')}</span>` : ''}</td></tr>
          <tr><td class="discreto">Memória</td>
              <td>${formatarBytes(a.memoria_total - a.memoria_disponivel)} em uso de ${formatarBytes(a.memoria_total)}</td></tr>
          ${a.swap_total ? `<tr><td class="discreto">Swap</td>
              <td>${formatarBytes(a.swap_usada)} de ${formatarBytes(a.swap_total)}</td></tr>` : ''}
          <tr><td class="discreto">Disco (${esc(a.disco_ponto || '—')})</td>
              <td>${formatarBytes(a.disco_total - a.disco_livre)} em uso de ${formatarBytes(a.disco_total)}</td></tr>
          <tr><td class="discreto">Rede</td>
              <td>saindo ${formatarBytes(a.rede_saida_bps)}/s · entrando ${formatarBytes(a.rede_entrada_bps)}/s</td></tr>
        </table>
      </div>` : `
      <div class="cartao">
        <h2>Números medidos</h2>
        <p class="discreto" style="margin:-8px 0 0">${esc(a.motivo || 'Indisponível neste sistema.')}</p>
      </div>`}

    <div class="cartao">
      <h2>O VOD Manager nesta máquina</h2>
      <table class="tabela-simples">
        <tr><td class="discreto" style="width:180px">Banco de dados</td>
            <td>${formatarBytes(d.contexto.tamanho_banco)}</td></tr>
        <tr><td class="discreto">Reproduções agora</td>
            <td>${num(d.contexto.streams_ativos)}</td></tr>
        <tr><td class="discreto">Sincronizando</td>
            <td>${d.contexto.sincronizando_agora ? 'sim' : 'não'}</td></tr>
        <tr><td class="discreto">Memória do processo</td>
            <td>${formatarBytes(a.processo_memoria)}</td></tr>
        <tr><td class="discreto">No ar há</td>
            <td>${duracaoHumana(a.uptime_segundos)}</td></tr>
        <tr><td class="discreto">Versão</td>
            <td class="mono">${esc(d.versao)} · ${esc(d.node)}</td></tr>
      </table>
    </div>

    <div class="cartao">
      <h2>Domínio e HTTPS</h2>
      <div id="area-dominio"><span class="discreto">Verificando…</span></div>
    </div>

    <div class="cartao">
      <h2>Dados entregues</h2>
      <div id="area-trafego"><span class="discreto">Carregando…</span></div>
    </div>

    <div class="cartao">
      <h2>Atualização</h2>
      <div id="area-atualizacao"><span class="discreto">Verificando…</span></div>
    </div>

    <div class="cartao">
      <h2>Migrar para outra máquina</h2>
      <div id="area-migracao"><span class="discreto">Verificando…</span></div>
    </div>

    <div class="cartao">
      <h2>Como escolher o tamanho da VPS</h2>
      <p class="discreto" style="margin:-8px 0 0">
        <b>A banda é o que satura primeiro</b>, não o processador. Hoje a entrega é direta
        da fonte: cada byte que chega ao espectador entrou vindo da sua fonte, então a
        banda conta duas vezes. Dez pessoas assistindo a 5 Mbps consomem cerca de 100 Mbps
        no total, e não 50.
        <br><br>
        <b>O processador quase não é usado para entregar vídeo</b> — a cópia dos bytes é
        barata e não há reencode. Ele pesa durante a sincronização, que é trabalho pontual.
        Se a CPU só sobe enquanto sincroniza, o caminho é agendar a sincronização para fora
        do horário de pico, não trocar de plano.
        <br><br>
        <b>A memória serve ao banco.</b> O que sobra vira cache das consultas; sem folga, a
        navegação no painel fica lenta em tudo. Swap em uso é sinal de que já faltou.
        <br><br>
        <b>O disco guarda o catálogo</b>, que é texto e ocupa pouco. Isso muda quando o
        cache de vídeo existir: aí o disco passa a ser dimensionado pelo acervo que você
        quer manter guardado.
      </p>
    </div>
  `;

  await desenharDominio();
  await desenharTrafego();
  await desenharAtualizacao();
  await desenharMigracao();

  // Recursos mudam continuamente; a tela acompanha.
  agendarAtualizacao('sistema', 5000);
}

// blocoAaPanel entrega o que fazer numa máquina onde o botão não pode agir.
//
// O aaPanel traz um nginx próprio, que já ocupa as portas 80 e 443. Um botão desligado com
// a explicação certa é honesto; um botão desligado e nada mais deixa a pessoa sem saída. As
// linhas abaixo são a saída — e o motivo de elas estarem aqui, e não só na documentação, é
// que as três do meio somem quando o aaPanel regenera a configuração do site, e o sintoma
// (o filme abre e corta depois de alguns minutos) não sugere a causa em nada.
function blocoAaPanel(porta) {
  const alvo = `http://127.0.0.1:${porta || 8080}`;
  const conf =
`    # ================= VOD Manager — início =================
    # Cole este bloco DENTRO do server { }, logo antes da última chave.
    # Não apague nada do que já está no arquivo: as linhas #SSL-START,
    # #CERT-APPLY-CHECK e #REWRITE são marcas que o aaPanel usa para se achar.

    # Os proxy_* ficam aqui em cima, no nível do server: assim valem para
    # todos os location abaixo sem precisar repetir.
    proxy_http_version 1.1;
    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    # Sem estas quatro linhas o vídeo abre e corta depois de alguns minutos:
    # o nginx tenta acumular a resposta antes de repassar, e a resposta aqui
    # é um filme inteiro.
    proxy_buffering off;
    proxy_request_buffering off;
    proxy_read_timeout 1h;
    proxy_send_timeout 1h;
    client_max_body_size 0;

    # O protocolo Xtream padronizou nomes terminados em .php. Eles NÃO são PHP
    # — mas o "include enable-php-XX.conf" que o aaPanel põe neste arquivo
    # captura tudo que termina em .php, e no nginx uma regra por expressão
    # regular ganha de "location /". Sem as três linhas abaixo, o painel abre,
    # a lista abre, e a API do Xtream responde erro de PHP.
    #
    # "location =" é a regra de maior prioridade do nginx: ganha até da
    # expressão regular. Por isso isto funciona sem mexer no que o aaPanel
    # gerencia — e continua funcionando se ele regenerar o arquivo.
    location = /get.php        { proxy_pass ${alvo}; }
    location = /player_api.php { proxy_pass ${alvo}; }
    location = /xmltv.php      { proxy_pass ${alvo}; }

    # O vídeo. "^~" impede que as regras de arquivo estático do aaPanel
    # (imagens, cache de 30 dias) capturem estes caminhos.
    location ^~ /movie/  { proxy_pass ${alvo}; }
    location ^~ /series/ { proxy_pass ${alvo}; }
    location ^~ /stream/ { proxy_pass ${alvo}; }

    # A folha de estilo e o script do painel.
    #
    # O aaPanel poe no arquivo do site uma regra assim:
    #     location ~ .*\\.(js|css)?$ { expires 12h; access_log off; }
    # Ela captura /app.css e /app.js e tenta servi-los da pasta do site, que
    # esta vazia. O painel abriria como uma pagina branca sem estilo nenhum, e
    # nada no navegador diria por que.
    location = /app.css { proxy_pass ${alvo}; }
    location = /app.js  { proxy_pass ${alvo}; }

    # O painel e todo o resto.
    location / { proxy_pass ${alvo}; }
    # ================== VOD Manager — fim ===================`;

  return `
    <div class="veredito alerta" style="margin:0 0 12px">
      <b>Esta máquina tem o aaPanel.</b>
      Ele traz um nginx próprio, que já responde pelas portas 80 e 443 — então quem
      configura o domínio aqui é ele, não este botão.
      <br><br>
      No aaPanel: <b>Website → Add site</b> com o seu domínio e <b>PHP: Static</b>. Depois
      <b>Website → o site → Config file</b> e cole o bloco abaixo <b>dentro do</b>
      <span class="mono">server { }</span> — role até o fim e cole logo <b>antes da última
      chave</b> <span class="mono">}</span>. Por fim <b>SSL → Let's Encrypt → Apply</b>.
      <br><br>
      <b>Não apague nada do que já está lá.</b> As linhas
      <span class="mono">#SSL-START</span>, <span class="mono">#CERT-APPLY-CHECK</span> e
      <span class="mono">#REWRITE-START</span> parecem comentários, mas são as marcas que o
      aaPanel usa para se localizar no arquivo. Apagá-las quebra a emissão do certificado.
      <br><br>
      Se houver um campo <b>Custom config</b> para o site, prefira colar lá: esse campo
      costuma sobreviver quando o aaPanel regenera o arquivo principal.
    </div>
    <textarea class="mono" rows="20" readonly style="font-size:12px">${esc(conf)}</textarea>
    <div class="grupo-botoes" style="justify-content:flex-start;margin:8px 0 4px">
      <button class="btn btn-mini" data-copiar-nginx="1">Copiar o bloco</button>
    </div>
    <p class="dica">
      <b>Guarde este bloco.</b> Se um dia o vídeo começar a cortar depois de alguns minutos,
      volte ao Config file e confira se as linhas continuam lá — o aaPanel pode tê-las
      apagado ao regenerar a configuração, e esse é o sintoma exato.
    </p>

    <div class="secao-titulo" style="margin:16px 0 6px">Depois de o site subir, faltam dois passos</div>
    <p class="discreto" style="margin:0 0 8px">
      Nenhum dos dois dá erro se for esquecido — e é justamente por isso que eles precisam
      estar escritos aqui.
    </p>
    <p class="dica" style="margin:0 0 10px">
      <b>1. Avisar o sistema de que há um proxy na frente.</b> Sem isto, todo cliente que
      entrar pelo domínio aparece como <span class="mono">127.0.0.1</span> — que é o nginx,
      não o espectador. O limite de telas por credencial deixa de distinguir gente, o limite
      de tentativas de login passa a ser compartilhado por todos, e as falhas de reprodução
      apontam sempre para a própria máquina. No terminal:
    </p>
    <textarea class="mono" rows="3" readonly style="font-size:12px">echo 'VODM_TRUST_PROXY=true' | sudo tee -a /etc/vodmanager.env
sudo systemctl restart vodmanager</textarea>
    <p class="dica" style="margin:10px 0 14px">
      <b>2. Corrigir o endereço público</b> em Configurações, para
      <span class="mono">https://seu.dominio</span>. É ele que vai dentro dos links de
      reprodução e das listas M3U. Se ficar com o endereço antigo, o painel abre, o catálogo
      aparece e <b>o vídeo não toca</b> — o sintoma mais confuso que existe, porque tudo o
      que você olha está certo.
    </p>`;
}

// desenharDominio monta a seção de domínio e HTTPS.
async function desenharDominio() {
  const area = $('#area-dominio');
  if (!area) return;

  let d;
  try {
    d = await api('/system/dominio');
  } catch (err) {
    area.innerHTML = `<div class="erro">${esc(err.message)}</div>`;
    return;
  }

  const registro = d.registro
    ? `<div class="secao-titulo" style="margin:14px 0 6px">Última configuração</div>
       <textarea class="mono" rows="10" readonly style="font-size:12px">${esc(d.registro)}</textarea>`
    : '';

  if (d.em_andamento) {
    area.innerHTML = `
      <div class="veredito alerta">
        <b>Configuração em andamento.</b>
        Leva um ou dois minutos. O acesso pelo IP continua funcionando o tempo todo.
      </div>
      ${registro}`;
    setTimeout(() => {
      if (estado.rota === 'sistema' && !atualizacaoAtrapalha()) desenharDominio();
    }, 5000);
    return;
  }

  area.innerHTML = `
    <p class="discreto" style="margin:-8px 0 12px">
      Aponte um domínio para esta máquina e o sistema instala o nginx, emite o certificado
      e liga o HTTPS. <b>O acesso pelo IP continua funcionando</b> — os links que os seus
      clientes já têm apontam para ele, e derrubá-los de uma vez seria o pior jeito de
      migrar.
    </p>
    ${d.aapanel ? blocoAaPanel(d.porta_local) : ''}
    ${d.disponivel ? `
      <label>Domínio
        <input id="dom-nome" placeholder="vod.seudominio.com" autocomplete="off">
      </label>
      <label>E-mail (opcional)
        <input id="dom-email" placeholder="voce@email.com" autocomplete="off">
      </label>
      <p class="dica">
        Antes de continuar, crie um registro <b>A</b> apontando o domínio para o IP desta
        máquina. O sistema confere isso primeiro e avisa se ainda não propagou — sem isso
        o certificado não é emitido.
        <br>
        <b>Pode informar mais de um nome</b>, separados por vírgula — o primeiro é o
        principal, o que aparece nos links. Os outros viram atalhos para o mesmo painel.
        O domínio raiz e o <span class="mono">www</span> são tentados sozinhos: se
        apontarem para esta máquina, entram junto. É o que faz digitar só
        <span class="mono">seudominio.com</span> abrir o painel — o nginx responde apenas
        pelos nomes exatos que conhece.
        <br>
        O e-mail recebe o aviso quando a renovação automática do certificado falhar. Sem
        ele, ninguém é avisado.
      </p>
      <div class="erro" id="dom-erro" hidden></div>
      <div class="grupo-botoes" style="justify-content:flex-start">
        <button class="btn btn-primario" id="dom-aplicar">Configurar domínio</button>
      </div>`
    : `<div class="erro">${esc(d.motivo)}</div>`}
    ${registro}`;

  $$('[data-copiar-nginx]').forEach(b => {
    b.onclick = () => copiar(area.querySelector('textarea.mono').value, b);
  });

  const botao = $('#dom-aplicar');
  if (!botao) return;

  botao.onclick = () => comAcao(async () => {
    const erro = $('#dom-erro');
    erro.hidden = true;
    const dominio = $('#dom-nome').value.trim();
    if (!dominio) {
      erro.textContent = 'Informe o domínio.';
      erro.hidden = false;
      return;
    }

    const ok = await confirmar('Configurar domínio',
      `O nginx será instalado e configurado para ${dominio}, e o certificado emitido. ` +
      'Se algo falhar, a configuração anterior é restaurada sozinha. ' +
      'O acesso pelo IP não é alterado.',
      'Configurar');
    if (!ok) return;

    botao.disabled = true;
    botao.textContent = 'Configurando…';
    try {
      const r = await api('/system/dominio', {
        method: 'POST',
        corpo: { dominio, email: $('#dom-email').value.trim() },
      });
      aviso(r.aviso, 'ok');
      desenharDominio();
    } catch (err) {
      erro.textContent = err.message;
      erro.hidden = false;
      botao.disabled = false;
      botao.textContent = 'Configurar domínio';
    }
  });
}

// desenharTrafego monta o resumo de dados entregues.
async function desenharTrafego() {
  const area = $('#area-trafego');
  if (!area) return;

  let t;
  try {
    t = await api('/trafego');
  } catch (err) {
    area.innerHTML = `<div class="erro">${esc(err.message)}</div>`;
    return;
  }

  area.innerHTML = `
    <div class="tabela-wrap"><table>
      <thead><tr>
        <th>Período</th>
        <th class="numero">Enviado aos clientes</th>
        <th class="numero">Recebido das fontes</th>
        <th class="numero">Reproduções</th>
        <th class="numero">Falhas</th>
      </tr></thead>
      <tbody>${t.periodos.map(p => `
        <tr>
          <td>${esc(p.periodo)}</td>
          <td class="numero"><b>${formatarBytes(p.bytes)}</b></td>
          <td class="numero">${t.entrega_direta ? formatarBytes(p.bytes) : '—'}</td>
          <td class="numero">${num(p.reproducoes)}</td>
          <td class="numero">${p.falhas > 0
               ? `<span class="etiqueta erro">${num(p.falhas)}</span>`
               : '0'}</td>
        </tr>`).join('')}
      </tbody></table></div>
    ${t.entrega_direta ? `
      <p class="dica" style="margin-top:8px">
        Como a entrega é <b>direta da fonte</b>, cada byte enviado a um cliente foi um byte
        recebido da sua fonte — as duas colunas são iguais, e o tráfego real da máquina é a
        <b>soma</b> das duas. Quando o cache existir, elas passam a divergir: é exatamente
        essa diferença que o cache economiza.
      </p>` : ''}`;
}

// desenharAtualizacao monta a seção de atualização do sistema.
async function desenharAtualizacao() {
  const area = $('#area-atualizacao');
  if (!area) return;

  let u;
  try {
    u = await api('/system/update');
  } catch (err) {
    area.innerHTML = `<div class="erro">${esc(err.message)}</div>`;
    return;
  }

  const registro = u.registro
    ? `<div class="secao-titulo" style="margin:14px 0 6px">Última atualização</div>
       <textarea class="mono" rows="10" readonly style="font-size:12px">${esc(u.registro)}</textarea>`
    : '';

  if (u.em_andamento) {
    area.innerHTML = `
      <div class="veredito alerta">
        <b>Atualização em andamento.</b>
        O serviço vai reiniciar e o painel pode ficar alguns segundos fora do ar.
        Esta tela acompanha sozinha.
      </div>
      ${registro}`;
    return;
  }

  area.innerHTML = `
    <p class="discreto" style="margin:-8px 0 12px">
      Versão instalada: <span class="mono">${esc(u.versao_atual)}</span>.
      Este botão faz <b>tudo o que o instalador faz</b>: busca a versão nova no GitHub,
      instala o que ela passou a precisar do sistema, compila, reaplica as configurações do
      serviço e reinicia. Não é preciso abrir terminal nem rodar o instalador de novo.
    </p>
    ${u.disponivel ? `
      <p class="dica" style="margin:0 0 10px">
        Um <b>backup é feito antes</b> de qualquer troca, a compilação acontece com o
        serviço ainda no ar, e <b>se a versão nova não subir em 30 segundos o sistema volta
        sozinho</b> para a anterior. Seus dados não são tocados.
      </p>
      <div class="grupo-botoes" style="justify-content:flex-start">
        <button class="btn btn-primario" id="btn-atualizar">Atualizar agora</button>
      </div>`
    : `<div class="erro">${esc(u.motivo)}</div>`}
    ${registro}`;

  const botao = $('#btn-atualizar');
  if (!botao) return;

  botao.onclick = () => comAcao(async () => {
    const ok = await confirmar('Atualizar o sistema',
      'O serviço vai reiniciar e quem estiver assistindo será desconectado. ' +
      'Um backup é feito antes, e o sistema volta sozinho para a versão atual se a nova falhar.',
      'Atualizar agora');
    if (!ok) return;

    botao.disabled = true;
    botao.textContent = 'Iniciando…';
    try {
      const r = await api('/system/update', { method: 'POST' });
      aviso(r.aviso, 'ok');
      acompanharAtualizacao();
    } catch (err) {
      aviso('Falha: ' + err.message, 'erro');
      botao.disabled = false;
      botao.textContent = 'Atualizar agora';
    }
  });
}

// desenharMigracao monta a seção de migração para outra máquina.
//
// A tela precisa dizer três coisas antes de qualquer campo, porque são as três que
// costumam ser descobertas tarde demais:
//
//   1. o servidor atual NÃO é desligado nem apagado — migrar é copiar, não mudar de casa;
//   2. os ids são preservados, então os links já entregues continuam apontando para o
//      mesmo filme e o mesmo episódio;
//   3. o nginx e o certificado não viajam. Quem atende por domínio precisa emitir o
//      certificado no destino DEPOIS de apontar o DNS, e essa ordem não pode inverter.
async function desenharMigracao() {
  const area = $('#area-migracao');
  if (!area) return;

  let m;
  try {
    m = await api('/system/migracao');
  } catch (err) {
    area.innerHTML = `<div class="erro">${esc(err.message)}</div>`;
    return;
  }

  const registro = m.registro
    ? `<div class="secao-titulo" style="margin:14px 0 6px">Última migração</div>
       <textarea class="mono" rows="14" readonly style="font-size:12px">${esc(m.registro)}</textarea>`
    : '';

  if (m.em_andamento) {
    area.innerHTML = `
      <div class="veredito alerta">
        <b>Migração em andamento.</b>
        Leva vários minutos: o destino instala o Postgres e o Go, compila o sistema e só
        então recebe os dados. <b>Este servidor continua no ar o tempo todo</b> — quem está
        assistindo não é interrompido. Esta tela acompanha sozinha.
      </div>
      ${registro}`;
    setTimeout(() => {
      if (estado.rota === 'sistema' && !atualizacaoAtrapalha()) desenharMigracao();
    }, 5000);
    return;
  }

  const avisoDominio = m.usa_dominio ? `
    <div class="veredito alerta" style="margin:0 0 12px">
      <b>Esta instalação atende pelo domínio ${esc(m.dominio_atual)}.</b>
      O nginx e o certificado são arquivos desta máquina, não do banco — eles não vão junto.
      Depois que a migração terminar, faça <b>nesta ordem</b>: aponte o DNS para o IP novo,
      espere resolver, e só então configure o domínio no painel do destino. Emitir o
      certificado antes do DNS apontar sempre falha, porque a validação acessa o domínio.
    </div>` : `
    <p class="dica" style="margin:0 0 12px">
      Esta instalação atende por IP. Os ids do catálogo são preservados, mas o
      <b>endereço</b> dentro dos links muda — eles apontam para este servidor. Se a sua meta
      é não mexer no XC_VM, configure um domínio <b>antes</b> de migrar: aí basta apontar o
      DNS depois e nenhum link precisa ser trocado.
    </p>`;

  area.innerHTML = `
    <p class="discreto" style="margin:-8px 0 12px">
      Leva para a outra máquina o catálogo inteiro <b>com os mesmos ids</b>, a chave de
      criptografia, os usuários do painel, as fontes, as credenciais de saída, o consumo já
      contado e as decisões que você tomou.
      <b>Nada aqui é desligado ou apagado</b> — os seus clientes continuam assistindo neste
      servidor enquanto você confere o outro.
    </p>
    <p class="dica" style="margin:0 0 12px">
      <b>Se a máquina de destino já tiver o VOD Manager</b>, nada é reinstalado lá: só os
      dados são trazidos, e o endereço público, o domínio e o certificado de lá ficam
      exatamente como estão. Serve para manter uma segunda máquina em dia — ela recebe as
      categorias, as pastas e as decisões que você tomou aqui desde a última vez, e leva
      minutos em vez de uma instalação inteira.
    </p>
    ${avisoDominio}
    ${m.disponivel ? `
      <label>IP da máquina de destino
        <input id="mig-destino" placeholder="203.0.113.10" autocomplete="off">
      </label>
      <div class="linha-campos">
        <label>Usuário do SSH
          <input id="mig-usuario" value="root" autocomplete="off">
        </label>
        <label>Porta do SSH
          <input id="mig-porta-ssh" value="22" inputmode="numeric" autocomplete="off">
        </label>
        <label>Porta do painel lá
          <input id="mig-porta-app" value="8080" inputmode="numeric" autocomplete="off">
        </label>
      </div>
      <label>Senha do SSH do destino
        <input type="password" id="mig-senha" autocomplete="new-password">
      </label>
      <p class="dica">
        É a senha de root que o provedor da VPS nova mandou. Ela é usada uma vez, para esta
        migração, e <b>não é guardada em lugar nenhum</b> — nem no banco, nem em
        configuração. Se preferir não digitá-la aqui, o mesmo trabalho é feito pelo
        terminal: <span class="mono">sudo ./scripts/migrar.sh --destino root@IP</span>
      </p>
      <div class="erro" id="mig-erro" hidden></div>
      <div class="grupo-botoes" style="justify-content:flex-start">
        <button class="btn btn-perigo" id="mig-aplicar">Migrar para esta máquina</button>
      </div>`
    : `<div class="erro">${esc(m.motivo)}</div>`}
    ${registro}`;

  const botao = $('#mig-aplicar');
  if (!botao) return;

  botao.onclick = () => comAcao(async () => {
    const erro = $('#mig-erro');
    erro.hidden = true;
    const destino = $('#mig-destino').value.trim();
    const senha = $('#mig-senha').value;
    if (!destino) {
      erro.textContent = 'Informe o IP da máquina de destino.';
      erro.hidden = false;
      return;
    }
    if (!senha) {
      erro.textContent = 'Informe a senha de SSH do destino.';
      erro.hidden = false;
      return;
    }

    const ok = await confirmar('Migrar para outra máquina',
      `Tudo o que está neste servidor será copiado para ${destino}. ` +
      'Se já houver uma instalação lá, os dados dela são SUBSTITUÍDOS pelos daqui — ' +
      'mas nada é reinstalado, e a configuração de endereço e domínio de lá é preservada. ' +
      'Este servidor não é alterado: continua no ar, com os dados intactos.',
      'Migrar agora');
    if (!ok) return;

    botao.disabled = true;
    botao.textContent = 'Iniciando…';
    try {
      const r = await api('/system/migracao', {
        method: 'POST',
        corpo: {
          destino,
          senha,
          usuario:   $('#mig-usuario').value.trim() || 'root',
          porta_ssh: Number($('#mig-porta-ssh').value) || 22,
          porta_app: Number($('#mig-porta-app').value) || 8080,
        },
      });
      // A senha sai da tela no instante em que deixa de ser necessária. Deixá-la num campo
      // é deixá-la no gerenciador de senhas do navegador, no histórico de formulário e à
      // vista de quem passar atrás de você.
      $('#mig-senha').value = '';
      aviso(r.aviso, 'ok');
      desenharMigracao();
    } catch (err) {
      erro.textContent = err.message;
      erro.hidden = false;
      botao.disabled = false;
      botao.textContent = 'Migrar para esta máquina';
    }
  });
}

// acompanharAtualizacao insiste na tela enquanto o serviço reinicia.
//
// Durante a troca do binário a API fica indisponível por alguns segundos, e uma falha de
// requisição nesse intervalo é esperada — não é erro. Por isso o laço ignora as falhas em
// vez de desistir na primeira.
async function acompanharAtualizacao() {
  for (let i = 0; i < 120; i++) {
    await new Promise(r => setTimeout(r, 2000));
    try {
      const u = await api('/system/update');
      if (!u.em_andamento) {
        aviso('Atualização concluída. Versão: ' + u.versao_atual, 'ok');
        navegar();
        return;
      }
    } catch {
      // servidor reiniciando
    }
  }
  navegar();
}

function duracaoHumana(segundos) {
  if (!segundos) return '—';
  const d = Math.floor(segundos / 86400);
  const h = Math.floor((segundos % 86400) / 3600);
  const m = Math.floor((segundos % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}min`;
  return `${m}min`;
}
// ---------------------------------------------------------------------------
// Tela: Acervo
// ---------------------------------------------------------------------------
//
// O acervo é o que ESTA operação guarda — em vez de puxar da fonte a cada reprodução.
//
// A tela separa duas coisas que parecem a mesma e não são:
//
//   CACHE          cópia de algo que veio de uma fonte. A fonte ainda tem o original, então
//                  apagar custa uma releitura. A limpeza automática cuida disto sozinha.
//
//   ACERVO PRÓPRIO arquivo que você colocou aqui. Não existe em lugar nenhum além desta
//                  instalação. Apagar é perda definitiva, e por isso NADA apaga sozinho —
//                  quando faltar espaço, a decisão vem para esta tela.
//
// A segunda é a razão de a tela existir. Um sistema que só apagasse cache resolveria o
// espaço sozinho e nunca precisaria perguntar nada.

const estadoAcervo = { aba: 'fonte' };

async function recarregarAcervo() {
  const y = window.scrollY || document.documentElement.scrollTop || 0;
  await verAcervo();
  window.scrollTo(0, y);
}

async function verAcervo() {
  const [resumo, { arquivos }, { sources }] = await Promise.all([
    api("/acervo"),
    api("/acervo/arquivos?limit=300&origem=" + estadoAcervo.aba),
    // A tela do Acervo nao pode cair porque a lista de fontes falhou: o acervo em si
    // continua servindo, e o cartao de fontes e informativo.
    api("/sources").catch(() => ({ sources: [] })),
  ]);

  const porOrigem = origem => resumo.resumo
    .filter(u => u.origem === origem)
    .reduce((soma, u) => ({ arquivos: soma.arquivos + u.arquivos, bytes: soma.bytes + u.bytes }),
            { arquivos: 0, bytes: 0 });

  const cache = porOrigem('fonte');
  const proprio = porOrigem('proprio');
  const esp = resumo.espaco_local;

  const aba = (valor, rotulo, conta) => `
    <button class="aba ${estadoAcervo.aba === valor ? 'ativa' : ''}" data-aba-acervo="${valor}">
      ${rotulo} <span class="aba-num">${num(conta.arquivos)}</span>
    </button>`;

  $('#visao').innerHTML = `
    ${!resumo.cache_ligado ? `
      <div class="cartao" style="margin-top:0">
        <div class="veredito alerta" style="margin:0">
          <b>O armazenamento está desligado.</b>
          Nada é copiado: cada reprodução puxa da fonte, como sempre foi. O que você vê
          abaixo continua sendo servido normalmente — desligar impede novas cópias, não
          apaga as que existem.
          <br><br>
          Para ligar: <b>Configurações → Armazenamento de mídia</b>. Depois marque, em cada
          fonte, quais podem ser copiadas — as duas coisas precisam estar ligadas.
        </div>
      </div>` : ''}

    <div class="secao-titulo">Espaço</div>
    <div class="grade-metricas">
      <div class="metrica">
        <div class="valor">${formatarBytes(cache.bytes)}</div>
        <div class="rotulo">Cache de fontes · ${num(cache.arquivos)} arquivo(s)</div>
      </div>
      <div class="metrica ${proprio.arquivos > 0 ? 'destaque' : ''}">
        <div class="valor">${formatarBytes(proprio.bytes)}</div>
        <div class="rotulo">Acervo próprio · ${num(proprio.arquivos)} arquivo(s)</div>
      </div>
      ${esp && !esp.ilimitado ? `
        <div class="metrica">
          <div class="valor">${formatarBytes(esp.livre)}</div>
          <div class="rotulo">
            Livre de ${formatarBytes(esp.total)}
            ${esp.pasta ? `<br><span class="dica">em ${esc(esp.pasta)}</span>` : ''}
          </div>
        </div>` : ''}
    </div>

    ${avisoDaLimpeza(resumo)}

    ${cartaoDeFontesDoCache(sources)}

    <div class="cartao">
      <h2>Contas de nuvem</h2>
      <div id="area-nuvens"><span class="discreto">Carregando…</span></div>
    </div>

    <div class="secao-titulo">Arquivos guardados</div>
    <div class="grupo-botoes" style="justify-content:flex-start;margin:0 0 10px">
      <button class="btn btn-primario" id="acervo-enviar">Enviar arquivo</button>
      <button class="btn" id="acervo-limpar-invalidas"
              title="Apaga as cópias pequenas demais para serem vídeo — restos de páginas de erro que a fonte devolveu no lugar do filme. Não toca no seu acervo próprio.">
        Limpar cópias inválidas</button>
    </div>
    <div class="abas">
      ${aba('fonte', 'Cache de fontes', cache)}
      ${aba('proprio', 'Acervo próprio', proprio)}
      <input class="busca-abas" id="acervo-busca" placeholder="Filtrar por título…" autocomplete="off">
    </div>

    <p class="discreto" style="margin:0 0 10px">
      ${estadoAcervo.aba === 'fonte' ? `
        Cópias de conteúdo que veio das suas fontes. <b>A limpeza automática apaga estas
        sozinha</b> quando faltar espaço, começando pelas menos usadas — a fonte ainda tem
        o original, então o custo de apagar é uma releitura.
        <br>
        <b>Proteger</b> tira um arquivo dessa regra. Serve para o caso que aparece sozinho
        com o tempo: a fonte tira o filme do ar, e a sua cópia passa a ser a única que
        existe.`
      : `
        Arquivos que você colocou aqui. <b>Nada apaga estes automaticamente</b>, nem quando
        o disco encher — eles não existem em lugar nenhum além desta instalação, e apagar é
        perda definitiva. Quando faltar espaço, a decisão aparece aqui, não no log.`}
    </p>

    ${arquivos.length ? `
      <div class="tabela-wrap"><table>
        <thead><tr>
          <th>Título</th><th>Onde está</th>
          <th class="numero">Tamanho</th><th class="numero">Acessos</th>
          <th>Estado</th><th style="width:1%"></th>
        </tr></thead>
        <tbody>${arquivos.map(a => linhaDoAcervo(a)).join('')}</tbody>
      </table></div>`
    : `<div class="cartao"><div class="vazio" style="padding:20px">
        <span class="icone">📼</span>
        <h3>Nada guardado ainda</h3>
        <p>${estadoAcervo.aba === 'fonte'
             ? 'As cópias aparecem aqui conforme os filmes forem assistidos, nas fontes que você marcar.'
             : 'Os arquivos que você enviar pelo painel aparecem aqui.'}</p>
      </div></div>`}
  `;

  ligarAcoesDoAcervo(resumo);
  desenharNuvens(resumo);

  // Enquanto houver cópia em andamento, a tela acompanha sozinha.
  //
  // Só nesse caso: um acervo parado não muda, e redesenhar de dez em dez segundos uma lista
  // que ninguém está alterando gasta banco e pisca a tela por nada. Com download em curso é
  // o contrário — a porcentagem parada é indistinguível de travamento.
  if (arquivos.some(a => a.estado === 'baixando' || a.estado === 'pendente' || a.estado === 'removendo')) {
    agendarAtualizacao('acervo', 10000);
  }
}

// linhaDoAcervo desenha um arquivo.
//
// O estado vem antes das ações de propósito: um download em curso não pode oferecer
// "apagar" com a mesma naturalidade de um arquivo pronto.
function linhaDoAcervo(a) {
  // "quente"/"arquivado" e não só o nome do lugar: agora as cópias MUDAM de camada sozinhas,
  // e sem essa palavra a tela não explica por que o mesmo filme aparecia no disco ontem e na
  // nuvem hoje — o que pareceria defeito em vez do funcionamento pretendido.
  const onde = a.backend === 'local'
    ? 'disco desta máquina <span class="dica">quente</span>'
    : `${a.nuvem_nome ? esc(a.nuvem_nome) : 'nuvem'} <span class="dica">arquivado</span>`;

  const progresso = a.bytes_totais && a.bytes_totais > 0
    ? Math.round((a.bytes_baixados / a.bytes_totais) * 100)
    : null;

  const estados = {
    pendente:  ['neutro', 'na fila'],
    baixando:  ['info', progresso !== null ? `baixando ${progresso}%` : 'baixando'],
    pronto:    ['ok', 'pronto'],
    erro:      ['erro', 'erro'],
    removendo: ['alerta', 'removendo'],
  };
  const [classe, rotulo] = estados[a.estado] || ['neutro', a.estado];

  return `
    <tr data-nome="${esc((a.titulo || '').toLowerCase())}">
      <td>
        <b>${esc(a.titulo || '(sem título)')}</b>
        ${a.protegido ? '<span class="etiqueta ok" style="margin-left:6px">protegido</span>' : ''}
        ${a.adiantado && !a.acessos
          ? '<span class="etiqueta alerta" style="margin-left:6px" title="Baixado por antecipação: é o episódio seguinte ao que alguém assistiu. Ninguém abriu ainda.">próxima reprodução</span>'
          : ''}
        ${a.fonte_nome ? `<div class="dica">de ${esc(a.fonte_nome)}</div>` : ''}
        ${motivoDoArquivo(a)}
      </td>
      <td class="discreto">${onde}</td>
      <td class="numero">
        ${a.estado === 'baixando'
          ? `<span class="discreto">${formatarBytes(a.bytes_baixados)}</span>
             ${a.bytes_totais ? ` de ${formatarBytes(a.bytes_totais)}` : ''}
             ${progresso !== null ? `
               <div class="barra-uso" style="margin-top:4px">
                 <div class="barra-uso-preenchida ok" style="width:${progresso}%"></div>
               </div>` : ''}`
          : formatarBytes(a.bytes)}
      </td>
      <td class="numero">${num(a.acessos)}</td>
      <td><span class="etiqueta ${classe}">${rotulo}</span></td>
      <td><div class="grupo-botoes">
        ${a.estado === 'erro' ? `
          <button class="btn btn-mini" data-tentar="${a.id}"
                  title="Devolve esta cópia à fila e zera as tentativas. Use quando a causa do erro já tiver passado.">
            Tentar de novo</button>` : ''}
        ${a.origem === 'fonte' ? `
          <button class="btn btn-mini" data-proteger="${a.id}" data-valor="${a.protegido ? 'false' : 'true'}"
                  data-titulo="${esc(a.titulo || '')}">
            ${a.protegido ? 'Desproteger' : 'Proteger'}</button>` : ''}
        <button class="btn btn-mini btn-perigo" data-apagar-arquivo="${a.id}"
                data-origem="${a.origem}" data-titulo="${esc(a.titulo || '')}"
                data-bytes="${a.bytes}">Apagar</button>
      </div></td>
    </tr>`;
}

// desenharNuvens monta a lista de contas.
//
// Mostra o que cada conta guarda ao lado do espaço dela: é a informação que falta na hora
// de decidir se cabe mais uma cópia, e a que responde "o que eu perco se remover esta?".
function desenharNuvens(resumo) {
  const area = $('#area-nuvens');
  if (!area) return;
  const nuvens = resumo.nuvens || [];

  area.innerHTML = `
    <p class="discreto" style="margin:-8px 0 12px">
      Você pode cadastrar <b>quantas contas quiser</b>. As cópias vão para a primeira conta
      ativa com espaço, na ordem abaixo — previsível de propósito: espalhar o acervo por
      todas faria perder uma conta significar perder um pedaço de tudo, em vez de perder as
      coisas mais antigas.
    </p>

    ${nuvens.length ? `
      <div class="tabela-wrap"><table>
        <thead><tr>
          <th class="numero">Ordem</th><th>Conta</th><th>Espaço</th>
          <th class="numero">Guardado aqui</th><th>Estado</th><th style="width:1%"></th>
        </tr></thead>
        <tbody>${nuvens.map(n => {
          const cota = espacoDaNuvem(n);
          let estado = '<span class="etiqueta ok">recebendo</span>';
          if (!n.ativa) estado = '<span class="etiqueta neutro">desativada</span>';
          else if (n.somente_leitura) estado = '<span class="etiqueta alerta">só leitura</span>';
          return `
            <tr>
              <td class="numero">${n.ordem}</td>
              <td><b>${esc(n.nome)}</b>
                  <div class="dica">${esc(n.provedor)}</div>
                  ${n.ultimo_erro ? `<div class="dica" style="color:var(--erro)">${esc(n.ultimo_erro)}</div>` : ''}</td>
              <td>${cota}</td>
              <td class="numero">${formatarBytes(n.bytes_guardados)}
                  <div class="dica">${num(n.arquivos)} arquivo(s)</div></td>
              <td>${estado}</td>
              <td><div class="grupo-botoes">
                ${n.pasta_raiz ? '' : `
                  <button class="btn btn-mini" data-nuvem-pasta="${n.id}"
                          title="Cria uma pasta &quot;VOD Manager&quot; na conta e passa a gravar dentro dela. Os arquivos que já estão na raiz continuam onde estão.">
                    Criar pasta</button>`}
                <button class="btn btn-mini" data-nuvem-leitura="${n.id}"
                        data-valor="${n.somente_leitura ? 'false' : 'true'}"
                        title="Para de receber cópias novas, sem parar de servir o que já está lá">
                  ${n.somente_leitura ? 'Voltar a receber' : 'Parar de receber'}</button>
                <button class="btn btn-mini" data-nuvem-esvaziar="${n.id}"
                        data-valor="${n.esvaziando ? 'false' : 'true'}"
                        data-nome="${esc(n.nome)}" data-arquivos="${n.arquivos}"
                        title="Move o acervo desta conta para outra, um arquivo por vez, em segundo plano">
                  ${n.esvaziando ? 'Parar de esvaziar' : 'Migrar para outra conta'}</button>
                <button class="btn btn-mini" data-nuvem-ativa="${n.id}" data-valor="${n.ativa ? 'false' : 'true'}">
                  ${n.ativa ? 'Desativar' : 'Ativar'}</button>
                <button class="btn btn-mini btn-perigo" data-nuvem-remover="${n.id}"
                        data-nome="${esc(n.nome)}" data-arquivos="${n.arquivos}">Remover</button>
              </div></td>
            </tr>`;
        }).join('')}
        </tbody></table></div>`
    : `<div class="vazio" style="padding:16px">
        <p class="discreto">Nenhuma conta cadastrada. O acervo fica no disco desta máquina.</p>
      </div>`}

    <div class="grupo-botoes" style="justify-content:flex-start;margin-top:12px">
      <button class="btn btn-primario" id="nuvem-adicionar">Adicionar conta</button>
    </div>`;

  ligarAcoesDasNuvens();
}

// abrirEnvioDeArquivo é o formulário de envio de vídeo.
//
// # Por que XMLHttpRequest e não fetch
//
// Só ele reporta progresso de UPLOAD. O fetch avisa quando termina, e num arquivo de 20 GB
// isso significa uma tela parada por meia hora — indistinguível de travada. Quem não vê
// progresso fecha a aba, e fechar a aba no meio perde o envio inteiro.
function abrirEnvioDeArquivo(categorias) {
  const opcoesCategoria = (categorias || [])
    .filter(c => c.principal && c.content_type === 'movie')
    .map(c => `<option value="${c.id}">${esc(c.name)}</option>`).join('');

  abrirModal('Enviar arquivo para o acervo', `
    <label>Arquivo de vídeo
      <input type="file" id="env-arquivo" accept="video/*,.mkv,.avi,.ts,.m4v,.mpg">
    </label>
    <p class="dica">
      mp4, mkv, avi, mov, ts, m4v, webm ou mpg. O envio vai <b>direto para o
      armazenamento</b> — o arquivo não passa pela memória do servidor, então o tamanho não
      é limitado por ela.
    </p>

    <label>Título
      <input id="env-titulo" placeholder="deixe vazio para usar o nome do arquivo" autocomplete="off">
    </label>

    <div class="linha-campos">
      <label>Ano (opcional)
        <input id="env-ano" inputmode="numeric" placeholder="2024" autocomplete="off">
      </label>
      <label>Onde guardar
        <select id="env-destino">
          <option value="local">Disco desta máquina</option>
          <option value="nuvem">Conta de nuvem</option>
        </select>
      </label>
    </div>

    ${opcoesCategoria ? `
      <label>Categoria (opcional)
        <select id="env-categoria">
          <option value="">— sem categoria —</option>
          ${opcoesCategoria}
        </select>
      </label>
      <p class="dica">Sem categoria, o filme existe no catálogo mas fica fora das pastas.</p>` : ''}

    <div class="veredito alerta" style="margin:6px 0">
      <b>Isto vira acervo próprio.</b> A limpeza automática nunca o apaga, nem com o disco
      cheio — porque ele não existe em nenhuma fonte, e apagar seria perda definitiva.
      Para removê-lo, só pela tela do Acervo.
    </div>

    <div id="env-progresso" hidden>
      <div class="barra-uso"><div class="barra-uso-preenchida ok" id="env-barra" style="width:0%"></div></div>
      <p class="dica" id="env-texto" style="margin-top:6px"></p>
    </div>

    <div class="erro" id="env-erro" hidden></div>
    <div class="grupo-botoes">
      <button class="btn" data-acao="cancelar">Cancelar</button>
      <button class="btn btn-primario" data-acao="enviar">Enviar</button>
    </div>
  `, corpo => {
    corpo.querySelector('[data-acao=cancelar]').onclick = fecharModal;
    corpo.querySelector('[data-acao=enviar]').onclick = e => {
      const erro = corpo.querySelector('#env-erro');
      erro.hidden = true;

      const campo = corpo.querySelector('#env-arquivo');
      const arquivo = campo.files && campo.files[0];
      if (!arquivo) {
        erro.textContent = 'Escolha o arquivo de vídeo.';
        erro.hidden = false;
        return;
      }

      // A ordem importa: o servidor lê as partes conforme elas chegam, e um título que
      // viesse DEPOIS do arquivo só seria conhecido com os gigabytes já gravados.
      const dados = new FormData();
      dados.append('titulo', corpo.querySelector('#env-titulo').value.trim());
      dados.append('ano', corpo.querySelector('#env-ano').value.trim());
      dados.append('backend', corpo.querySelector('#env-destino').value);
      const cat = corpo.querySelector('#env-categoria');
      if (cat) dados.append('categoria_id', cat.value);
      dados.append('arquivo', arquivo, arquivo.name);

      const progresso = corpo.querySelector('#env-progresso');
      const barra = corpo.querySelector('#env-barra');
      const texto = corpo.querySelector('#env-texto');
      progresso.hidden = false;
      e.target.disabled = true;
      e.target.textContent = 'Enviando…';
      campo.disabled = true;

      const req = new XMLHttpRequest();
      req.open('POST', '/api/v1/acervo/enviar');
      req.withCredentials = true;

      req.upload.onprogress = ev => {
        if (!ev.lengthComputable) return;
        const pct = Math.round((ev.loaded / ev.total) * 100);
        barra.style.width = pct + '%';
        texto.textContent = `${pct}% — ${formatarBytes(ev.loaded)} de ${formatarBytes(ev.total)}`;
      };
      // O último byte enviado não é o fim: o servidor ainda está gravando no destino. Dizer
      // "100%" e ficar parado pareceria travamento justo no momento mais frágil.
      req.upload.onload = () => {
        texto.textContent = 'Enviado. Gravando no destino…';
      };

      req.onload = () => {
        if (req.status >= 200 && req.status < 300) {
          fecharModal();
          aviso('Arquivo enviado e já no catálogo.', 'ok');
          recarregarAcervo();
          return;
        }
        let msg = `O servidor respondeu ${req.status}.`;
        try { msg = JSON.parse(req.responseText).error.message || msg; } catch { /* corpo não-JSON */ }
        erro.textContent = msg;
        erro.hidden = false;
        progresso.hidden = true;
        e.target.disabled = false;
        e.target.textContent = 'Enviar';
        campo.disabled = false;
      };
      req.onerror = () => {
        erro.textContent = 'A conexão caiu durante o envio. Nada foi gravado no catálogo.';
        erro.hidden = false;
        progresso.hidden = true;
        e.target.disabled = false;
        e.target.textContent = 'Enviar';
        campo.disabled = false;
      };

      req.send(dados);
    };
  });
}


// ligarAcoesDoAcervo conecta os botões da lista de arquivos.
function ligarAcoesDoAcervo(resumo) {
  const enviar = $("#acervo-enviar");
  if (enviar) enviar.onclick = () => abrirEnvioDeArquivo(resumo && resumo.categorias);

  const limpar = $('#acervo-limpar-invalidas');
  if (limpar) limpar.onclick = async () => {
    limpar.disabled = true;
    try {
      const r = await api('/acervo/limpar-invalidas', { method: 'POST' });
      aviso(r.removidas > 0
        ? `${r.removidas} cópia(s) inválida(s) marcadas para remoção. Os títulos voltam a ` +
          `ser buscados na fonte e serão copiados de novo na próxima reprodução.`
        : 'Nenhuma cópia inválida encontrada.', 'ok');
      recarregarAcervo();
    } catch (err) {
      aviso('Falha: ' + err.message, 'erro');
      limpar.disabled = false;
    }
  };

  $$('[data-aba-acervo]').forEach(b => {
    b.onclick = () => {
      estadoAcervo.aba = b.dataset.abaAcervo;
      window.scrollTo(0, 0);
      verAcervo();
    };
  });

  // A marca de cache por fonte, editável aqui além da tela de Fontes.
  //
  // Marcar não copia nada retroativamente: as cópias nascem das reproduções seguintes. Por
  // isso o aviso — sem ele, a expectativa vira "marquei e não baixou nada".
  $$('[data-cache-fonte]').forEach(cx => {
    cx.onchange = async () => {
      const antes = !cx.checked;
      cx.disabled = true;
      try {
        await api(`/sources/${cx.dataset.cacheFonte}`, {
          method: 'PATCH', corpo: { cache_habilitado: cx.checked },
        });
        verAcervo();
      } catch (e) {
        cx.checked = antes;
        cx.disabled = false;
        aviso(`Não foi possível alterar: ${e.message}`, "erro");
      }
    };
  });

  const busca = $('#acervo-busca');
  if (busca) busca.oninput = () => {
    const termo = busca.value.trim().toLowerCase();
    $$('tr[data-nome]').forEach(tr => {
      tr.hidden = termo !== '' && !tr.dataset.nome.includes(termo);
    });
  };

  $$('[data-proteger]').forEach(b => {
    b.onclick = () => comAcao(async () => {
      const ligar = b.dataset.valor === 'true';
      try {
        await api(`/acervo/arquivos/${b.dataset.proteger}/proteger`, {
          method: 'PUT', corpo: { protegido: ligar },
        });
        aviso(ligar
          ? `"${b.dataset.titulo}" não será mais apagado pela limpeza automática.`
          : `"${b.dataset.titulo}" voltou a ser descartável.`, 'ok');
        recarregarAcervo();
      } catch (err) { aviso('Falha: ' + err.message, 'erro'); }
    });
  });

  // Apagar tem dois avisos diferentes, e a diferença é a coisa mais importante desta tela.
  //
  // Cache: a fonte ainda tem o original, então o custo é uma releitura. Acervo próprio: não
  // existe em lugar nenhum além daqui, e não há de onde trazer de volta.
  $$('[data-tentar]').forEach(b => {
    b.onclick = async () => {
      b.disabled = true;
      try {
        await api(`/acervo/arquivos/${b.dataset.tentar}/tentar`, { method: 'POST' });
        aviso('De volta à fila. O baixador pega em até trinta segundos.', 'ok');
        recarregarAcervo();
      } catch (err) {
        aviso('Falha: ' + err.message, 'erro');
        b.disabled = false;
      }
    };
  });

  $$('[data-apagar-arquivo]').forEach(b => {
    b.onclick = () => comAcao(async () => {
      const proprio = b.dataset.origem === 'proprio';
      const ok = await confirmar(
        proprio ? 'Apagar do acervo próprio' : 'Apagar do cache',
        proprio
          ? `"${b.dataset.titulo}" foi enviado por você e não existe em nenhum outro lugar. ` +
            'Apagar é definitivo: não há como trazer de volta, nem pelo backup — o backup ' +
            'guarda o registro do arquivo, não o vídeo.'
          : `"${b.dataset.titulo}" volta a ser puxado da fonte quando alguém assistir. ` +
            `Libera ${formatarBytes(Number(b.dataset.bytes) || 0)}.`,
        'Apagar');
      if (!ok) return;
      b.disabled = true;
      try {
        await api(`/acervo/arquivos/${b.dataset.apagarArquivo}`, { method: 'DELETE' });
        aviso('Marcado para remoção.', 'ok');
        recarregarAcervo();
      } catch (err) {
        aviso('Falha: ' + err.message, 'erro');
        b.disabled = false;
      }
    });
  });
}

// ligarAcoesDasNuvens conecta os botões das contas.
function ligarAcoesDasNuvens() {
  const ajustar = async (id, corpo, mensagem) => {
    try {
      await api(`/nuvens/${id}`, { method: 'PATCH', corpo });
      aviso(mensagem, 'ok');
      recarregarAcervo();
    } catch (err) { aviso('Falha: ' + err.message, 'erro'); }
  };

  $$('[data-nuvem-pasta]').forEach(b => {
    b.onclick = () => comAcao(async () => {
      b.disabled = true;
      try {
        await api(`/nuvens/${b.dataset.nuvemPasta}/pasta`, { method: 'POST' });
        aviso('Pasta criada. As próximas cópias vão para dentro dela; as que já estão na ' +
              'raiz continuam onde estão e seguem sendo servidas.', 'ok');
        recarregarAcervo();
      } catch (err) {
        aviso('Falha: ' + err.message, 'erro');
        b.disabled = false;
      }
    });
  });

  // Migrar o acervo de uma conta para outra.
  //
  // A confirmação diz o que vai acontecer com números, e não em abstrato: "1 arquivo" e
  // "4.000 arquivos" são a mesma frase e decisões muito diferentes. E diz o mais importante
  // — nada é apagado, e a conta segue servindo enquanto move.
  $$('[data-nuvem-esvaziar]').forEach(b => {
    b.onclick = () => comAcao(async () => {
      const ligar = b.dataset.valor === 'true';
      if (ligar) {
        const n = Number(b.dataset.arquivos) || 0;
        const ok = await confirmar(`Migrar o acervo de "${b.dataset.nome}"?`,
          `${n} arquivo(s) serão movidos para a próxima conta com espaço — ou para o disco ` +
          `desta máquina, se não houver outra conta.\n\n` +
          `Acontece em segundo plano, um arquivo por vez, e pode levar horas. Nada é ` +
          `apagado: a conta continua servindo o que ainda não foi movido, e você pode parar ` +
          `a qualquer momento.\n\n` +
          `Enquanto migra, esta conta para de receber cópias novas.`,
          'Migrar');
        if (!ok) return;
      }
      try {
        await api(`/nuvens/${b.dataset.nuvemEsvaziar}/esvaziar`,
          { method: 'POST', corpo: { esvaziar: ligar } });
        aviso(ligar
          ? 'Migração iniciada. Acompanhe pela contagem de arquivos desta conta.'
          : 'Migração interrompida. O que já foi movido continua no destino.', 'ok');
        recarregarAcervo();
      } catch (err) { aviso('Falha: ' + err.message, 'erro'); }
    });
  });

  $$('[data-nuvem-leitura]').forEach(b => {
    b.onclick = () => comAcao(() => {
      const parar = b.dataset.valor === 'true';
      return ajustar(b.dataset.nuvemLeitura, { somente_leitura: parar },
        parar ? 'A conta parou de receber cópias novas. O que já está lá continua sendo servido.'
              : 'A conta voltou a receber cópias.');
    });
  });

  $$('[data-nuvem-ativa]').forEach(b => {
    b.onclick = () => comAcao(async () => {
      const ativar = b.dataset.valor === 'true';
      if (!ativar) {
        const ok = await confirmar('Desativar a conta',
          'Desativar tira a conta do ar por inteiro: o que está guardado nela PARA de ser ' +
          'servido. Se a intenção é só não receber mais cópias, use "Parar de receber" — ' +
          'esse mantém tudo funcionando.',
          'Desativar');
        if (!ok) return;
      }
      await ajustar(b.dataset.nuvemAtiva, { ativa: ativar },
        ativar ? 'Conta ativada.' : 'Conta desativada.');
    });
  });

  $$('[data-nuvem-remover]').forEach(b => {
    b.onclick = () => comAcao(async () => {
      const quantos = Number(b.dataset.arquivos) || 0;
      if (quantos > 0) {
        aviso(`"${b.dataset.nome}" ainda guarda ${num(quantos)} arquivo(s). ` +
              'Apague-os na lista abaixo, ou use "Desativar" em vez de remover.', 'erro');
        return;
      }
      const ok = await confirmar('Remover a conta',
        `A conta "${b.dataset.nome}" será removida do sistema. As credenciais guardadas ` +
        'aqui são apagadas — nada é apagado dentro da conta em si.', 'Remover');
      if (!ok) return;
      try {
        await api(`/nuvens/${b.dataset.nuvemRemover}`, { method: 'DELETE' });
        aviso('Conta removida.', 'ok');
        recarregarAcervo();
      } catch (err) { aviso('Falha: ' + err.message, 'erro'); }
    });
  });

  const adicionar = $('#nuvem-adicionar');
  if (adicionar) adicionar.onclick = () => abrirCadastroDeNuvem();
}

// abrirCadastroDeNuvem guia o cadastro de uma conta de nuvem.
//
// O roteiro fica na tela, e não num documento, porque ele tem um passo que erra sozinho: o
// endereço de retorno precisa ser IDÊNTICO no Google e aqui, letra por letra. O painel
// mostra o dele já montado, com botão de copiar — o que elimina a causa mais comum de a
// autorização falhar com uma mensagem que não explica nada.
function abrirCadastroDeNuvem() {
  const retorno = location.origin + '/api/v1/nuvens/oauth/retorno';

  abrirModal('Adicionar conta do Google Drive', `
    <p class="discreto" style="margin:-4px 0 4px">
      São 5 minutos, uma vez por conta. Depois disso ela renova sozinha e você não mexe
      mais nisso.
    </p>

    <div class="secao-titulo" style="margin:10px 0 0">1. Criar o projeto no Google</div>
    <p class="dica" style="margin-top:4px">
      Abra <b>console.cloud.google.com</b>, crie um projeto (qualquer nome), e ative a
      <b>Google Drive API</b> em <i>APIs e serviços → Biblioteca</i>.
    </p>

    <div class="secao-titulo" style="margin:12px 0 0">2. Tela de consentimento</div>
    <p class="dica" style="margin-top:4px">
      Em <i>APIs e serviços → Tela de permissão OAuth</i>, escolha <b>Externo</b> e
      preencha o nome do app e o seu e-mail.
      <br>
      Em <b>Usuários de teste</b>, acrescente <b>a conta do Google cujo Drive você vai
      usar</b>. Sem isso o Google recusa a autorização com "app não verificado" — e é o
      tropeço mais comum aqui.
    </p>

    <div class="secao-titulo" style="margin:12px 0 0">3. Criar as credenciais</div>
    <p class="dica" style="margin-top:4px">
      Em <i>Credenciais → Criar credenciais → ID do cliente OAuth</i>, escolha o tipo
      <b>Aplicativo da Web</b>.
      <br><br>
      Em <b>URIs de redirecionamento autorizados</b>, cole exatamente isto:
    </p>
    <div style="display:flex;gap:8px;align-items:center;margin:6px 0">
      <input class="mono" id="nv-retorno" readonly value="${esc(retorno)}" style="flex:1">
      <button class="btn btn-mini" data-copiar-retorno="1">Copiar</button>
    </div>
    <p class="dica">
      Precisa ser <b>igual, caractere por caractere</b>. Se o painel for acessado por outro
      endereço depois, acrescente esse outro também.
      <br>
      <b>Não use "Conta de serviço"</b>: arquivos criados por ela pertencem a ela, que tem
      cota zero — os seus terabytes não seriam usados.
    </p>

    <div class="secao-titulo" style="margin:12px 0 0">4. Preencher aqui</div>
    <label>Nome desta conta
      <input id="nv-nome" placeholder="Drive principal" autocomplete="off">
    </label>
    <p class="dica">
      É como você distingue esta das outras. <b>Não pode ser alterado depois</b>: o nome faz
      parte da proteção das credenciais guardadas.
    </p>

    <label>Client ID<input id="nv-cid" autocomplete="off"></label>
    <label>Client Secret<input type="password" id="nv-csec" autocomplete="new-password"></label>

    <div class="linha-campos">
      <label>Pasta no Drive (opcional)
        <input id="nv-pasta" placeholder="id da pasta" autocomplete="off">
      </label>
      <label>Ordem de preenchimento
        <input id="nv-ordem" value="100" inputmode="numeric" autocomplete="off">
      </label>
    </div>
    <p class="dica">
      A pasta confina o acervo. Não é organização — é limite de dano: a conta tem outras
      coisas dentro, e um erro nosso não pode alcançá-las. O id está na URL quando você abre
      a pasta no Drive, depois de <span class="mono">/folders/</span>. Em branco, usa a raiz.
      <br>
      A ordem só importa com mais de uma conta: a menor recebe primeiro.
    </p>

    <div class="veredito ok" style="margin:10px 0">
      O sistema pede acesso <b>apenas aos arquivos que ele mesmo criar</b>. Seus documentos,
      fotos e planilhas ficam invisíveis para ele — não é promessa nossa, é o Google que não
      os entrega.
    </div>

    <div class="erro" id="nv-erro" hidden></div>
    <div class="grupo-botoes">
      <button class="btn" data-acao="cancelar">Cancelar</button>
      <button class="btn btn-primario" data-acao="autorizar">Autorizar no Google</button>
    </div>
  `, corpo => {
    corpo.querySelector('[data-copiar-retorno]').onclick = e =>
      copiar(retorno, e.target);
    corpo.querySelector('[data-acao=cancelar]').onclick = fecharModal;

    corpo.querySelector('[data-acao=autorizar]').onclick = async e => {
      const erro = corpo.querySelector('#nv-erro');
      erro.hidden = true;

      const dados = {
        nome: corpo.querySelector('#nv-nome').value.trim(),
        client_id: corpo.querySelector('#nv-cid').value.trim(),
        client_secret: corpo.querySelector('#nv-csec').value.trim(),
        pasta_raiz: corpo.querySelector('#nv-pasta').value.trim(),
        ordem: Number(corpo.querySelector('#nv-ordem').value) || 100,
      };
      if (!dados.nome || !dados.client_id || !dados.client_secret) {
        erro.textContent = 'Preencha o nome, o client id e o client secret.';
        erro.hidden = false;
        return;
      }

      e.target.disabled = true;
      try {
        const r = await api('/nuvens/oauth/iniciar', { method: 'POST', corpo: dados });
        // Janela nova, e não redirecionamento: o painel continua aberto atrás, e uma
        // autorização abandonada não custa o que estava sendo feito aqui.
        window.open(r.url, 'autorizar-drive', 'width=560,height=680');
        fecharModal();
        aviso('Autorize na janela do Google. Quando terminar, atualize esta tela.', 'ok');
      } catch (err) {
        erro.textContent = err.message;
        erro.hidden = false;
        e.target.disabled = false;
      }
    };
  });
}

// ---------------------------------------------------------------------------
// Tela: Configurações
// ---------------------------------------------------------------------------

async function verConfiguracoes() {
  const c = await api('/settings');

  $('#visao').innerHTML = `
    ${c.endereco_atual_diverge ? `
      <div class="cartao" style="margin-top:0;border-color:#4a3a12">
        <h2>⚠️ As listas estão entregando um endereço diferente do seu</h2>
        <p class="discreto" style="margin:-8px 0 12px">
          Você entrou no painel por <span class="mono">${esc(c.endereco_atual)}</span>, mas
          as listas M3U e os links de reprodução saem com
          <span class="mono">${esc(c.content_base_url_em_uso)}</span>.
          <br><br>
          Isso acontece quando um domínio novo entra na frente do sistema — configurado
          aqui, pelo aaPanel ou por outro proxy — ou depois de uma migração de máquina:
          nada disso mexe nestas configurações sozinho. <b>Nada acusa o problema</b>: o
          painel abre, o catálogo aparece, e quem descobre é o cliente.
          <br><br>
          Trocar agora é o que faz o endereço parar de importar: com o domínio nos links,
          uma mudança de servidor vira só uma troca de DNS, e <b>nenhum cliente precisa
          mexer em nada</b>.
        </p>
        <div class="grupo-botoes" style="justify-content:flex-start">
          <button class="btn btn-primario" id="usar-endereco-atual">
            Passar a entregar ${esc(c.endereco_atual)}
          </button>
        </div>
        <p class="dica" style="margin-top:8px">
          Os links que os seus clientes já têm <b>continuam funcionando</b> — o endereço
          antigo não é desligado. Muda o que sai daqui em diante.
        </p>
      </div>` : ''}

    <div class="cartao" ${c.endereco_atual_diverge ? '' : 'style="margin-top:0"'}>
      <h2>Endereço público deste servidor</h2>
      <p class="discreto" style="margin:-8px 0 14px">
        É o endereço que aparece nos links de reprodução — o que você cadastra no XC_VM.
        Precisa ser um endereço que <b>outras máquinas conseguem alcançar</b>: o IP do
        servidor ou um domínio. <span class="mono">localhost</span> só funciona dentro
        desta máquina.
      </p>

      ${c.public_base_url_e_local ? `
        <div class="erro" style="margin-bottom:14px">
          Em uso agora: <span class="mono">${esc(c.public_base_url_em_uso)}</span> —
          <b>os links não vão funcionar fora desta máquina.</b>
        </div>` : `
        <div style="margin-bottom:14px;color:var(--ok)">
          Em uso agora: <span class="mono">${esc(c.public_base_url_em_uso)}</span>
        </div>`}

      <label>Endereço do painel
        <input id="cfg-base" value="${esc(c.public_base_url)}"
               placeholder="http://198.51.100.10:8080">
      </label>
      <p class="dica">
        Exemplos: <span class="mono">http://192.168.1.50:8080</span> na sua rede local,
        <span class="mono">http://200.1.2.3:8080</span> num servidor com IP público, ou
        <span class="mono">https://vod.seudominio.com</span> com domínio e HTTPS.
        Deixe vazio para usar o endereço da requisição.
      </p>
      ${c.definido_por_ambiente ? `
        <p class="dica">
          Há também um valor definido por variável de ambiente. O que você gravar aqui
          tem precedência sobre ele.
        </p>` : ''}

      <div class="secao-titulo" style="margin:18px 0 8px">Endereço do conteúdo</div>
      <p class="discreto" style="margin:-4px 0 10px">
        O endereço que aparece nos <b>links de vídeo, listas M3U e Xtream</b> — o que vai
        para as mãos dos seus clientes.
        <br><br>
        Vale usar um domínio diferente do painel. O link do vídeo chega ao cliente, e com
        ele o cliente descobre onde fica o sistema — inclusive a tela de administração.
        Com dois domínios, o que você entrega não revela por onde você administra.
      </p>
      <label>Endereço do conteúdo
        <input id="cfg-conteudo" value="${esc(c.content_base_url || '')}"
               placeholder="https://tv.seudominio.com">
      </label>
      <p class="dica">
        Em uso agora: <span class="mono">${esc(c.content_base_url_em_uso)}</span>.
        Deixe vazio para usar o mesmo endereço do painel.
        <br>
        Este domínio precisa apontar para <b>esta mesma máquina</b> — ele não é um
        redirecionamento, é outro nome para o mesmo servidor.
      </p>

      <div class="erro" id="cfg-erro" hidden></div>
      <div class="grupo-botoes" style="justify-content:flex-start">
        <button class="btn btn-primario" id="cfg-salvar">Salvar</button>
      </div>
    </div>

    <div class="cartao">
      <h2>Armazenamento de mídia</h2>
      <p class="discreto" style="margin:-8px 0 14px">
        Ligado, o sistema guarda uma cópia do que for assistido e passa a servir dela — sem
        comprar a banda da fonte de novo a cada reprodução. É também o que mantém um filme
        no ar depois de a fonte tirá-lo.
        <br><br>
        Esta é a <b>chave geral</b>. Ela não basta sozinha: cada fonte tem a sua, e as duas
        precisam estar ligadas para uma cópia acontecer. A geral existe para você poder
        parar tudo de uma vez — quando o disco encher, quando algo estiver errado — sem
        precisar lembrar quais fontes você marcou meses atrás.
      </p>

      <label class="linha-check">
        <input type="checkbox" id="cfg-cache" ${c.cache_ligado ? 'checked' : ''}>
        Guardar mídia no acervo
      </label>

      <div class="linha-campos" style="margin-top:12px">
        <label>Destino padrão
          <select id="cfg-cache-destino">
            <option value="local" ${c.cache_backend === 'local' ? 'selected' : ''}>Disco desta máquina</option>
            <option value="nuvem" ${c.cache_backend === 'nuvem' ? 'selected' : ''}>Conta de nuvem</option>
          </select>
        </label>
        <label>Carência antes de poder apagar (horas)
          <input id="cfg-cache-carencia" type="number" min="0" value="${esc(c.cache_idade_minima_horas || '24')}">
        </label>
        <label>Folga mínima do armazenamento (%)
          <input id="cfg-cache-folga" type="number" min="0" max="90" value="${esc(c.cache_espaco_minimo_pct || '10')}">
        </label>
        <label>Teto do cache (GB)
          <input id="cfg-cache-teto" type="number" min="0" step="1"
                 value="${Math.round((Number(c.cache_limite_bytes) || 0) / (1024 ** 3))}">
        </label>
      </div>

      ${c.cache_backend === 'nuvem' ? `
        <div class="veredito alerta" style="margin:4px 0 12px">
          <b>Com a nuvem como destino, nada passa pelo disco.</b>
          Todo conteúdo é gravado direto na conta, e toda reprodução do cache paga a
          latência dela — inclusive os títulos mais assistidos, que são justamente os que o
          disco atenderia em milissegundos.
          <br><br>
          Para as duas camadas — rápido no disco, frio na nuvem — escolha
          <b>Disco desta máquina</b>. A nuvem continua sendo usada: ela recebe tudo o que
          esfriar.
        </div>` : ''}

      <label class="linha-check" style="margin:4px 0 10px">
        <input type="checkbox" id="cfg-cache-arquivar"
               ${c.cache_arquivar_sempre ? 'checked' : ''}>
        Mandar para a nuvem assim que passar a carência, sem esperar o disco encher
      </label>
      <p class="dica" style="margin:-4px 0 12px">
        Sem isto, o sistema só arquiva quando o disco aperta — e aperto já significa cópias
        falhando por falta de espaço. Com disco pequeno, marcar mantém o disco sempre
        folgado para o que estiver quente agora. Custa banda: cada arquivo sobe para a nuvem
        mesmo quando ainda cabia aqui.
      </p>

      <label class="linha-check" style="margin:4px 0 10px">
        <input type="checkbox" id="cfg-cache-adiantar-nuvem"
               ${c.cache_adiantar_na_nuvem ? 'checked' : ''}>
        Baixar o próximo episódio direto na nuvem, sem passar pelo disco
      </label>
      <p class="dica" style="margin:-4px 0 12px">
        O adiantamento é uma aposta: ninguém abriu aquele episódio ainda, e o espectador pode
        largar a série no anterior. Marcando, o disco fica livre para quem está assistindo
        agora.
        <br>
        Perde-se pouco. O que o adiantamento entrega não é armazenamento rápido — é
        <b>não precisar da fonte</b>, que responde em mais de um segundo e corta entregas no
        meio. A nuvem responde em centenas de milissegundos e não corta.
        <br>
        Sem conta de nuvem cadastrada, ele continua indo para o disco: adiantar no disco é
        melhor que não adiantar.
      </p>

      <div class="veredito info" style="margin:4px 0 12px">
        <b>Quando a limpeza começa.</b> Os dois campos acima são gatilhos, e vale o que
        disparar primeiro:
        <br>
        • <b>Folga mínima</b> — quando sobrar menos que essa fatia do armazenamento.
        Serve para o disco da máquina, que também precisa de espaço para o banco de dados.
        <br>
        • <b>Teto do cache</b> — quando as cópias somarem esse tamanho, mesmo com disco
        sobrando. Serve para não deixar o acervo crescer sem limite. <b>0 desliga</b> este
        gatilho, valendo só a folga.
      </div>

      <p class="dica">
        Disparado o gatilho, a ordem é sempre a mesma, da menor perda para a maior:
        <b>mover o mais frio para a nuvem</b> (nada se perde), depois <b>apagar o mais
        frio</b> — menos acessado e acessado há mais tempo. Ele nunca apaga um filme
        procurado para caber um que ninguém pediu ainda.
        <br>
        A <b>carência</b> protege o que é recente. Sem ela o cache entra em vaivém: guarda um
        filme, apaga dez minutos depois para caber outro, e na hora seguinte faz o inverso —
        gasta banda dos dois lados e não melhora nada.
        <br>
        <b>A limpeza automática só apaga cache.</b> O que você enviar pelo painel nunca é
        apagado sozinho, nem com o disco cheio; a decisão aparece na tela do Acervo.
      </p>

      <div class="erro" id="cfg-cache-erro" hidden></div>
      <div class="grupo-botoes" style="justify-content:flex-start">
        <button class="btn btn-primario" id="cfg-cache-salvar">Salvar</button>
        <button class="btn btn-sutil" onclick="location.hash='#/acervo'">Ver o acervo</button>
      </div>
    </div>

    <div class="cartao">
      <h2>Sua senha do painel</h2>
      <p class="discreto" style="margin:-8px 0 14px">
        Trocar a senha encerra todas as sessões, inclusive esta — você vai precisar entrar
        de novo. É de propósito: quem troca a senha por desconfiar de um acesso não pode
        deixar a sessão do invasor viva.
      </p>
      <label>Senha atual <input type="password" id="pw-atual" autocomplete="current-password"></label>
      <label>Nova senha <input type="password" id="pw-nova" autocomplete="new-password"></label>
      <label>Repita a nova senha <input type="password" id="pw-repete" autocomplete="new-password"></label>
      <p class="dica">Ao menos 12 caracteres.</p>
      <div class="erro" id="pw-erro" hidden></div>
      <div class="grupo-botoes" style="justify-content:flex-start">
        <button class="btn btn-primario" id="pw-salvar">Trocar senha</button>
      </div>
    </div>

    <div class="cartao">
      <h2>Como o conteúdo é entregue hoje</h2>
      <p class="discreto" style="margin:-8px 0 0">
        <b>Direto da fonte, sem guardar nada.</b> O VOD Manager busca os bytes da sua
        fonte e repassa ao player, escondendo a origem. Nada é gravado em disco.
        <br><br>
        A consequência: cada pessoa assistindo abre uma conexão à sua fonte. Dez pessoas
        no mesmo filme são dez conexões.
        <br><br>
        Na próxima etapa isso passa a ser configurável por fonte, com três modos:
        <b>sempre direto</b> (nunca guarda), <b>guardar quando alguém assistir</b>
        (grava na primeira vez e serve do disco depois) e <b>guardar sempre</b>.
        Fontes com conexão ilimitada podem ficar no direto; as limitadas se beneficiam
        de guardar.
      </p>
    </div>
  `;

  $('#cfg-salvar').onclick = async e => {
    const erro = $('#cfg-erro');
    erro.hidden = true;
    e.target.disabled = true;
    try {
      await api('/settings', {
        method: 'PUT',
        corpo: {
          public_base_url: $('#cfg-base').value.trim(),
          content_base_url: $('#cfg-conteudo').value.trim(),
        },
      });
      aviso('Endereço salvo. Os links já usam o novo valor.', 'ok');
      navegar();
    } catch (err) {
      erro.textContent = err.message;
      erro.hidden = false;
      e.target.disabled = false;
    }
  };

  // As configurações do acervo salvam sozinhas, sem os endereços.
  //
  // Pelo mesmo endpoint, mas mandando só os campos do acervo: o servidor só toca no que
  // vem no corpo. Sem essa separação, salvar aqui reescreveria os endereços com o que
  // estivesse nos campos acima — inclusive vazio, se alguém tivesse limpado sem salvar.
  $('#cfg-cache-salvar').onclick = async e => {
    const erro = $('#cfg-cache-erro');
    erro.hidden = true;
    e.target.disabled = true;
    try {
      await api('/settings', {
        method: 'PUT',
        corpo: {
          public_base_url: c.public_base_url,
          content_base_url: c.content_base_url || '',
          cache_ligado: $('#cfg-cache').checked,
          cache_backend: $('#cfg-cache-destino').value,
          cache_idade_minima_horas: Number($('#cfg-cache-carencia').value) || 0,
          cache_espaco_minimo_pct: Number($("#cfg-cache-folga").value) || 0,
          // Em GB na tela, em bytes no banco: ninguem digita 53687091200 sem errar um zero.
          cache_limite_bytes: Math.round((Number($("#cfg-cache-teto").value) || 0) * (1024 ** 3)),
          cache_arquivar_sempre: $("#cfg-cache-arquivar").checked,
          cache_adiantar_na_nuvem: $("#cfg-cache-adiantar-nuvem").checked,
        },
      });
      aviso($('#cfg-cache').checked
        ? 'Armazenamento ligado. Marque em cada fonte quais podem ser guardadas.'
        : 'Armazenamento desligado. As cópias que já existem continuam sendo servidas.', 'ok');
      navegar();
    } catch (err) {
      erro.textContent = err.message;
      erro.hidden = false;
      e.target.disabled = false;
    }
  };

  // Um clique para adotar o endereço pelo qual você entrou.
  //
  // Limpa o endereço de conteúdo em vez de repeti-lo: ele existe para quem separa o
  // domínio do painel do domínio do conteúdo, e vazio significa "use o mesmo". Deixar uma
  // cópia guardada faria a próxima troca de domínio arrumar um campo e esquecer o outro —
  // que é exatamente a situação que este botão está desfazendo.
  const adotar = $('#usar-endereco-atual');
  if (adotar) adotar.onclick = () => comAcao(async () => {
    adotar.disabled = true;
    adotar.textContent = 'Salvando…';
    try {
      await api('/settings', {
        method: 'PUT',
        corpo: { public_base_url: c.endereco_atual, content_base_url: '' },
      });
      aviso('Pronto. As listas e os links passam a entregar ' + c.endereco_atual + '.', 'ok');
      navegar();
    } catch (err) {
      aviso('Falha: ' + err.message, 'erro');
      adotar.disabled = false;
      adotar.textContent = 'Passar a entregar ' + c.endereco_atual;
    }
  });

  $('#pw-salvar').onclick = async e => {
    const erro = $('#pw-erro');
    erro.hidden = true;

    const nova = $('#pw-nova').value;
    // Confere a repetição aqui: o servidor não recebe o campo, e errar a digitação da
    // senha nova seria descobrir só na hora de entrar de novo.
    if (nova !== $('#pw-repete').value) {
      erro.textContent = 'A nova senha e a repetição não conferem.';
      erro.hidden = false;
      return;
    }

    e.target.disabled = true;
    try {
      await api('/auth/change-password', {
        method: 'POST',
        corpo: { current_password: $('#pw-atual').value, new_password: nova },
      });
      aviso('Senha trocada. Entre novamente.', 'ok');
      setTimeout(() => location.reload(), 1200);
    } catch (err) {
      erro.textContent = err.message;
      erro.hidden = false;
      e.target.disabled = false;
    }
  };
}

// ---------------------------------------------------------------------------
// Tela: Reproduções em andamento
// ---------------------------------------------------------------------------


async function verStreams() {
  const d = await api('/streams');
  const s = d.stats;

  const metrica = (valor, rotulo, destaque) => `
    <div class="metrica ${destaque ? 'destaque' : ''}">
      <div class="valor">${valor}</div><div class="rotulo">${rotulo}</div>
    </div>`;

  $('#visao').innerHTML = `
    <div class="grade-metricas">
      ${metrica(num(s.active), 'Reproduzindo agora')}
      ${metrica(num(s.last_24h), 'Reproduções (24h)')}
      ${metrica(formatarBytes(s.bytes_served_24h), 'Transferido (24h)')}
      ${metrica(s.avg_ttfb_ms != null ? s.avg_ttfb_ms + ' ms' : '—', 'Tempo até o 1º byte')}
      ${metrica(num(s.errors_24h), 'Erros (24h)', s.errors_24h > 0)}
    </div>

    <div class="cartao">
      <h2>O que acontece quando alguém assiste</h2>
      <p class="discreto" style="margin:-8px 0 0">
        O VOD Manager <b>não redireciona</b> o player para a sua fonte, e <b>ainda não
        guarda nada em disco</b>. Os bytes passam através do servidor: ele busca da fonte e
        repassa ao player, escondendo a origem.
        <br><br>
        A consequência é que <b>cada espectador abre uma conexão à sua fonte</b> — dez
        pessoas no mesmo filme são dez conexões. É exatamente isso que o cache
        (Fase 5) elimina: uma conexão à fonte, o arquivo guardado em disco, e todos os
        demais servidos localmente.
      </p>
    </div>

    <div class="secao-titulo">Reproduzindo agora</div>
    ${d.active.length ? `
      <div class="tabela-wrap"><table>
        <thead><tr>
          <th>Conteúdo</th><th>Fonte</th><th>Cliente</th><th>Entrega</th>
          <th class="numero">Enviado</th><th class="numero">1º byte</th><th>Início</th>
        </tr></thead>
        <tbody>${d.active.map(a => `
          <tr>
            <td><b>${esc(a.title)}</b></td>
            <td class="discreto">${esc(a.source_name) || '—'}</td>
            <td class="mono discreto">${esc(a.client_ip)}</td>
            <td>${etiquetaEntrega(a.cache_result)}</td>
            <td class="numero">${formatarBytes(a.bytes_sent)}</td>
            <td class="numero">${a.ttfb_ms != null ? a.ttfb_ms + ' ms' : '—'}</td>
            <td class="discreto">${tempoRelativo(a.started_at)}</td>
          </tr>`).join('')}
        </tbody></table></div>`
      : `<div class="cartao"><div class="vazio">
          <span class="icone">📺</span>
          <h3>Nada sendo reproduzido</h3>
          <p>Abra um filme e clique em <b>Link de reprodução</b> para testar num player.</p>
        </div></div>`}
  `;

  // Reprodução é estado que muda a cada segundo; a tela precisa acompanhar.
  agendarAtualizacao('streams', 2000);
}

function etiquetaEntrega(resultado) {
  const mapa = {
    passthrough: ['info', 'direto da fonte'],
    hit: ['ok', 'do cache'],
    miss: ['alerta', 'baixando e servindo'],
  };
  const [classe, rotulo] = mapa[resultado] || ['neutro', resultado];
  return `<span class="etiqueta ${classe}">${esc(rotulo)}</span>`;
}

// ---------------------------------------------------------------------------
// Tela: Credenciais de saída
// ---------------------------------------------------------------------------

// cotaEsgotada repete no navegador a regra que o servidor aplica.
//
// Duplicar a lógica não é ideal, mas a alternativa seria uma consulta por linha só para
// pintar um rótulo. A regra é curta e está testada do lado do servidor, que é quem manda.
function cotaEsgotada(c) {
  if (!c.bytes_limit) return false;
  if (c.ciclo === 'mensal') {
    const inicio = new Date(c.ciclo_inicio);
    const agora = new Date();
    const inicioDoMes = new Date(agora.getFullYear(), agora.getMonth(), 1);
    if (inicio < inicioDoMes) return false; // o mês virou: cota renovada
  }
  return c.bytes_ciclo >= c.bytes_limit;
}

// celulaDeCota mostra o consumo do jeito que a pergunta é feita: "quanto falta?".
function celulaDeCota(c) {
  if (!c.bytes_limit) {
    return `${formatarBytes(c.bytes_served)}<div class="dica">sem limite</div>`;
  }
  // Quando o mês virou, o consumo do ciclo anterior não conta mais.
  const renovou = c.ciclo === 'mensal' && !cotaEsgotada(c) && c.bytes_ciclo >= c.bytes_limit;
  const usado = renovou ? 0 : c.bytes_ciclo;
  const pct = Math.min(100, Math.round((usado / c.bytes_limit) * 100));
  const nivel = pct >= 100 ? 'erro' : pct >= 80 ? 'alerta' : 'ok';
  return `
    <b>${formatarBytes(usado)}</b> de ${formatarBytes(c.bytes_limit)}
    <div class="barra-uso"><div class="barra-uso-preenchida ${nivel}"
         style="width:${pct}%"></div></div>
    <div class="dica">${pct}%${c.ciclo === 'mensal' ? ' · renova dia 1º' : ''}</div>`;
}

async function verCredenciais() {
  const { credentials } = await api('/stream-credentials');

  $('#acoes-pagina').innerHTML =
    '<button class="btn btn-primario" id="nova-credencial">+ Nova credencial</button>';
  $('#nova-credencial').onclick = formularioCredencialStreaming;

  const estadoDe = c => {
    if (c.revoked_at) return '<span class="etiqueta erro">revogada</span>';
    // Cota esgotada é situação diferente de revogada: uma é "acabou o pacote", a outra é
    // "você perdeu o acesso". Quem atende o cliente precisa distinguir de relance.
    if (cotaEsgotada(c)) return '<span class="etiqueta alerta">cota esgotada</span>';
    if (!c.enabled) return '<span class="etiqueta neutro">desativada</span>';
    if (c.expires_at && new Date(c.expires_at) < new Date()) {
      return '<span class="etiqueta alerta">expirada</span>';
    }
    return '<span class="etiqueta ok">ativa</span>';
  };

  $('#visao').innerHTML = `
    <p class="discreto" style="margin:0 0 14px">
      São as credenciais que o <b>XC_VM</b> — ou um cliente seu — usa para pedir vídeo ao
      VOD Manager. Não têm relação com as credenciais das suas fontes: estas são de saída,
      aquelas de entrada.
      <br><br>
      <b>Uma credencial por cliente.</b> Assim você vê quanto cada um consome, limita
      quantas telas ele pode usar ao mesmo tempo, e corta o acesso dele sozinho — sem
      afetar os demais.
      <br><br>
      O botão <b>Lista</b> entrega o catálogo inteiro em cada credencial: um endereço de
      lista M3U e os dados para cadastrar como servidor Xtream. A mesma credencial serve
      para a lista e para assistir — revogá-la corta as duas coisas.
    </p>

    ${credentials.length ? `
      <div class="tabela-wrap"><table>
        <thead><tr>
          <th>Nome</th><th>Usuário</th><th>Estado</th>
          <th class="numero">Assistindo</th><th class="numero">Limite</th>
          <th class="numero">Usos</th><th>Consumo</th>
          <th>Último uso</th><th style="width:1%"></th>
        </tr></thead>
        <tbody>${credentials.map(({ credential: c, active_connections: ativas }) => `
          <tr>
            <td><b>${esc(c.name)}</b>${c.description ? `<div class="discreto">${esc(c.description)}</div>` : ''}</td>
            <td class="mono discreto">${esc(c.username)}</td>
            <td>${estadoDe(c)}</td>
            <td class="numero">${ativas > 0 ? `<span class="etiqueta ok">${num(ativas)}</span>` : '0'}</td>
            <td class="numero">${c.max_connections != null ? num(c.max_connections) : '<span class="discreto">sem limite</span>'}</td>
            <td class="numero">${num(c.use_count)}</td>
            <td>${celulaDeCota(c)}</td>
            <td class="discreto">${tempoRelativo(c.last_used_at)}</td>
            <td><div class="grupo-botoes">
              <button class="btn btn-mini" data-lista="${c.id}">Lista</button>
              <button class="btn btn-mini" data-editar-cred="${c.id}">Editar</button>
              ${!c.revoked_at ? `<button class="btn btn-mini btn-primario" data-rotacionar="${c.id}" data-nome="${esc(c.name)}">Nova senha</button>` : ''}
              ${!c.revoked_at ? `<button class="btn btn-mini btn-perigo" data-revogar="${c.id}" data-nome="${esc(c.name)}">Revogar</button>` : ''}
              <button class="btn btn-mini" data-excluir-cred="${c.id}" data-nome="${esc(c.name)}">Excluir</button>
            </div></td>
          </tr>`).join('')}
        </tbody></table></div>`
      : `<div class="cartao"><div class="vazio">
          <span class="icone">🔑</span>
          <h3>Nenhuma credencial de saída</h3>
          <p>Crie uma para gerar o link permanente que você vai cadastrar no XC_VM.</p>
          <button class="btn btn-primario" onclick="document.getElementById('nova-credencial').click()">Criar a primeira</button>
        </div></div>`}
  `;

  const porID = id => credentials.find(x => String(x.credential.id) === String(id)).credential;

  $$('[data-rotacionar]').forEach(b => {
    b.onclick = async () => {
      const ok = await confirmar('Gerar nova senha',
        `Gerar uma senha nova para "${b.dataset.nome}"? A senha atual para de funcionar ` +
        `imediatamente e quem estiver assistindo é desconectado. O usuário e o link continuam os mesmos.`,
        'Gerar nova senha');
      if (!ok) return;
      try {
        const r = await api(`/stream-credentials/${b.dataset.rotacionar}/rotate`, { method: 'POST' });
        mostrarCredencialCriada(r);
      } catch (err) { aviso('Falha: ' + err.message, 'erro'); }
    };
  });

  $$('[data-editar-cred]').forEach(b => {
    b.onclick = () => formularioEditarCredencial(porID(b.dataset.editarCred));
  });

  $$('[data-lista]').forEach(b => {
    b.onclick = () => mostrarLinksDaLista(porID(b.dataset.lista));
  });

  $$('[data-revogar]').forEach(b => {
    b.onclick = async () => {
      const ok = await confirmar('Revogar credencial',
        `Revogar "${b.dataset.nome}"? Quem estiver usando este link perde o acesso em segundos. ` +
        `Não dá para desfazer — seria preciso criar uma credencial nova.`, 'Revogar');
      if (!ok) return;
      try {
        await api(`/stream-credentials/${b.dataset.revogar}/revoke`, { method: 'POST' });
        aviso('Credencial revogada.', 'ok');
        navegar();
      } catch (err) { aviso('Falha: ' + err.message, 'erro'); }
    };
  });

  $$('[data-excluir-cred]').forEach(b => {
    b.onclick = async () => {
      const ok = await confirmar('Excluir credencial',
        `Excluir "${b.dataset.nome}" definitivamente?`, 'Excluir');
      if (!ok) return;
      try {
        await api(`/stream-credentials/${b.dataset.excluirCred}`, { method: 'DELETE' });
        aviso('Credencial excluída.', 'ok');
        navegar();
      } catch (err) { aviso('Falha: ' + err.message, 'erro'); }
    };
  });
}

function formularioEditarCredencial(c) {
  abrirModal(`Editar ${c.name}`, `
    <label>Nome <input id="ec-nome" value="${esc(c.name)}"></label>
    <label>Descrição <input id="ec-desc" value="${esc(c.description)}"
             placeholder="Ex.: cliente João — plano 2 telas"></label>
    <label>Máximo de reproduções simultâneas
      <input id="ec-max" type="number" min="1" value="${c.max_connections ?? ''}"
             placeholder="vazio = sem limite">
    </label>
    <p class="dica">
      É o que impede o cliente de repassar a senha: passando do limite, as reproduções
      extras recebem recusa. Deixe vazio para não limitar.
    </p>
    <div class="secao-titulo" style="margin:18px 0 8px">Cota de banda</div>
    <label>Limite em GB
      <input id="ec-cota" type="number" min="0" step="0.5"
             value="${c.bytes_limit ? (c.bytes_limit / 1073741824).toFixed(1) : ''}"
             placeholder="vazio ou 0 = sem limite">
    </label>
    <label>Renovação
      <select id="ec-ciclo">
        <option value="nenhum" ${c.ciclo !== 'mensal' ? 'selected' : ''}>
          Não renova — quando acabar, acabou
        </option>
        <option value="mensal" ${c.ciclo === 'mensal' ? 'selected' : ''}>
          Mensal — zera todo dia 1º
        </option>
      </select>
    </label>
    <p class="dica">
      Ao atingir a cota, a lista e o vídeo param de funcionar para este cliente até você
      aumentar o limite, zerar o ciclo, ou o mês virar.
      ${c.bytes_limit ? `<br>Consumo no ciclo atual: <b>${formatarBytes(c.bytes_ciclo)}</b> de ${formatarBytes(c.bytes_limit)}.` : ''}
    </p>
    ${c.bytes_ciclo > 0 ? `
      <label class="linha-check">
        <input type="checkbox" id="ec-zerar"> Zerar o consumo agora
      </label>
      <p class="dica">
        Recomeça a contagem sem esperar a virada do mês — é o caminho de "o cliente pagou
        um pacote extra".
      </p>` : ''}

    <div class="secao-titulo" style="margin:18px 0 8px">Endereço de entrega</div>
    <label>Endereço só desta credencial (opcional)
      <input id="ec-base" placeholder="https://vod.exemplo.com  ou  http://203.0.113.9:8080"
             value="${esc(c.base_url_override || '')}">
    </label>
    <p class="dica">
      Em branco usa o endereço de conteúdo global — o caso normal.
      <br>
      Preencha para dar a <b>este cliente</b> um caminho próprio: por exemplo o IP direto, sem
      passar pelo proxy reverso, para quem você confia e cujo player não se importa com
      certificado. Precisa começar com <b>http://</b> ou <b>https://</b>.
      <br>
      Lembre que um IP não sobrevive a uma troca de máquina, e o domínio sim.
    </p>

    <div class="secao-titulo" style="margin:18px 0 8px">Estado</div>
    <label class="linha-check">
      <input type="checkbox" id="ec-ativa" ${c.enabled ? 'checked' : ''}> Credencial ativa
    </label>
    <p class="dica">
      Desativar é reversível — útil para suspender por falta de pagamento. Revogar é
      definitivo.
    </p>
    <div class="erro" id="ec-erro" hidden></div>
    <div class="grupo-botoes">
      <button class="btn" data-acao="cancelar">Cancelar</button>
      <button class="btn btn-primario" data-acao="salvar">Salvar</button>
    </div>
  `, corpo => {
    corpo.querySelector('[data-acao=cancelar]').onclick = fecharModal;
    corpo.querySelector('[data-acao=salvar]').onclick = async e => {
      const erro = corpo.querySelector('#ec-erro');
      erro.hidden = true;
      e.target.disabled = true;

      const bruto = corpo.querySelector('#ec-max').value.trim();
      const cota = corpo.querySelector('#ec-cota').value.trim();
      const zerar = corpo.querySelector('#ec-zerar');
      const dados = {
        name: corpo.querySelector('#ec-nome').value,
        description: corpo.querySelector('#ec-desc').value,
        enabled: corpo.querySelector('#ec-ativa').checked,
        max_connections: bruto === '' ? null : Number(bruto),
        // Zero significa "sem limite": mais natural que apagar o campo, e sem a
        // ambiguidade entre "não mexer" e "remover".
        bytes_limit_gb: cota === '' ? 0 : Number(cota),
        ciclo: corpo.querySelector('#ec-ciclo').value,
        zerar_ciclo: !!(zerar && zerar.checked),
        base_url_override: corpo.querySelector("#ec-base").value.trim(),
      };
      try {
        await api(`/stream-credentials/${c.id}`, { method: 'PATCH', corpo: dados });
        fecharModal();
        aviso('Credencial atualizada.', 'ok');
        navegar();
      } catch (err) {
        erro.textContent = err.message;
        erro.hidden = false;
        e.target.disabled = false;
      }
    };
  });
}

function formularioCredencialStreaming() {
  abrirModal('Nova credencial de saída', `
    <p class="discreto">
      Deixe usuário e senha em branco para o sistema gerar os dois com segurança. Preencha
      se você já vende acesso e quer manter o login que o cliente conhece.
    </p>
    <label>Nome <input id="sc-nome" placeholder="Ex.: João da Silva" required></label>
    <label>Descrição <input id="sc-desc" placeholder="opcional"></label>
    <label>Usuário <input id="sc-user" class="mono" placeholder="em branco = gerado automaticamente"></label>
    <label>Senha <input id="sc-senha" class="mono" placeholder="em branco = gerada automaticamente"></label>
    <p class="dica">
      Usuário e senha viajam dentro do endereço do vídeo, então aceitam apenas letras,
      números, ponto, hífen e sublinhado. A senha precisa de ao menos 8 caracteres.
    </p>
    <div class="erro" id="sc-erro" hidden></div>
    <div class="grupo-botoes">
      <button class="btn" data-acao="cancelar">Cancelar</button>
      <button class="btn btn-primario" data-acao="criar">Criar</button>
    </div>
  `, corpo => {
    corpo.querySelector('[data-acao=cancelar]').onclick = fecharModal;
    corpo.querySelector('[data-acao=criar]').onclick = async e => {
      const erro = corpo.querySelector('#sc-erro');
      erro.hidden = true;
      e.target.disabled = true;
      try {
        const r = await api('/stream-credentials', {
          method: 'POST',
          corpo: {
            name: corpo.querySelector('#sc-nome').value,
            description: corpo.querySelector('#sc-desc').value,
            username: corpo.querySelector('#sc-user').value.trim(),
            password: corpo.querySelector('#sc-senha').value.trim(),
          },
        });
        mostrarCredencialCriada(r);
      } catch (err) {
        erro.textContent = err.message;
        erro.hidden = false;
        e.target.disabled = false;
      }
    };
  });
}

function mostrarCredencialCriada(r) {
  abrirModal('Credencial criada', `
    <label>Usuário
      <input class="mono" readonly value="${esc(r.username)}" onclick="this.select()">
    </label>
    <label>Senha
      <input class="mono" readonly value="${esc(r.password)}" onclick="this.select()">
    </label>
    <div class="grupo-botoes" style="justify-content:flex-start">
      <button class="btn btn-mini" data-copiar-user>Copiar usuário</button>
      <button class="btn btn-primario btn-mini" data-copiar-senha>Copiar senha</button>
    </div>
    <p class="dica">
      Você não precisa anotar: o botão <b>Lista</b> desta credencial mostra a senha e os
      endereços prontos sempre que precisar.
    </p>
    <div class="grupo-botoes">
      <button class="btn" data-acao="fechar">Fechar</button>
      <button class="btn btn-primario" data-acao="links">Ver os links de acesso</button>
    </div>
  `, corpo => {
    corpo.querySelector('[data-copiar-user]').onclick = e => copiar(r.username, e.target);
    corpo.querySelector('[data-copiar-senha]').onclick = e => copiar(r.password, e.target);
    corpo.querySelector('[data-acao=fechar]').onclick = () => { fecharModal(); navegar(); };
    corpo.querySelector('[data-acao=links]').onclick = () => {
      fecharModal();
      mostrarLinksDaLista(r.credential);
    };
  });
}

function formatarBytes(n) {
  if (!n) return '0 B';
  const unidades = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < unidades.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(i === 0 ? 0 : 1)} ${unidades[i]}`;
}

// ---------------------------------------------------------------------------
// Tela: Sincronizações
// ---------------------------------------------------------------------------

async function verSincronizacoes() {
  const { runs } = await api('/sync/runs?limit=100');
  if (!runs.length) {
    $('#visao').innerHTML = `<div class="cartao"><div class="vazio">
      <span class="icone">🔄</span><h3>Nenhuma sincronização</h3>
      <p>Vá em Fontes e clique em Sincronizar para executar a primeira.</p>
    </div></div>`;
    return;
  }
  $('#visao').innerHTML = `
    ${tabelaRuns(runs)}
    ${runs.some(r => r.error_message) ? `
      <div class="cartao">
        <h2>Execuções com problema</h2>
        ${runs.filter(r => r.error_message).map(r => `
          <div style="margin-bottom:12px">
            <b>${esc(r.source_name)}</b> ${etiquetaEstadoRun(r.state)}
            <span class="discreto">— ${dataHora(r.started_at)}</span>
            <div class="erro" style="margin-top:6px">${esc(r.error_message)}</div>
          </div>`).join('')}
      </div>` : ''}`;
}

// ---------------------------------------------------------------------------
// Tela: Eventos
// ---------------------------------------------------------------------------

let filtroEventos = '';

async function verEventos() {
  const { events } = await api('/events?limit=200' + (filtroEventos ? `&category=${filtroEventos}` : ''));

  $('#visao').innerHTML = `
    <div class="barra-ferramentas">
      <select id="filtro-cat">
        <option value="">Todas as categorias</option>
        ${['auth', 'source', 'sync'].map(c =>
          `<option value="${c}" ${filtroEventos === c ? 'selected' : ''}>${c}</option>`).join('')}
      </select>
    </div>
    ${events.length ? `
      <div class="tabela-wrap"><table>
        <thead><tr><th>Quando</th><th>Nível</th><th>Categoria</th><th>Mensagem</th><th>Autor</th></tr></thead>
        <tbody>${events.map(e => `
          <tr>
            <td class="discreto" style="white-space:nowrap">${dataHora(e.ts)}</td>
            <td><span class="etiqueta ${e.level === 'error' ? 'erro' : e.level === 'warn' ? 'alerta' : 'neutro'}">${esc(e.level)}</span></td>
            <td class="discreto">${esc(e.category)}</td>
            <td>${esc(e.message)}</td>
            <td class="discreto">${esc(e.actor) || '—'}</td>
          </tr>`).join('')}
        </tbody></table></div>`
      : '<div class="cartao"><div class="vazio"><span class="icone">📋</span><h3>Nenhum evento</h3></div></div>'}`;

  $('#filtro-cat').onchange = e => { filtroEventos = e.target.value; navegar(); };
}

// ---------------------------------------------------------------------------
// Barra global de sincronização
// ---------------------------------------------------------------------------

/**
 * Mantém uma barra no rodapé enquanto houver sincronização rodando, em qualquer tela.
 *
 * A sincronização acontece no servidor; o painel só observa. Sem esta barra, o
 * administrador precisava manter a janela de progresso aberta para saber que algo estava
 * em andamento — e ao fechá-la, parecia que nada acontecia.
 */
function iniciarBarraSincronizacao() {
  const barra = $('#barra-sync');
  const texto = $('#barra-sync-texto');
  const detalhes = $('#barra-sync-detalhes');
  let ultimaRun = null;

  detalhes.onclick = () => {
    if (ultimaRun) acompanharSincronizacao(ultimaRun.id, ultimaRun.source_name);
  };

  async function verificar() {
    if (!estado.usuario) return;
    try {
      const { runs } = await api('/sync/runs?limit=10');
      const ativa = runs.find(r => r.state === 'running');

      if (!ativa) {
        if (ultimaRun) {
          // Acabou de terminar: atualiza a tela para refletir os números finais.
          ultimaRun = null;
          barra.classList.add('hidden');
          document.body.classList.remove('com-barra-sync');
          if (!estado.ocupado) navegar();
        }
        return;
      }

      const anterior = ultimaRun;
      ultimaRun = ativa;
      barra.classList.remove('hidden');
      document.body.classList.add('com-barra-sync');

      const segundos = Math.max(1, Math.round((Date.now() - new Date(ativa.started_at)) / 1000));
      const velocidade = Math.round(ativa.items_seen / segundos);
      const partes = [
        `Sincronizando <b>${esc(ativa.source_name)}</b>`,
        `${num(ativa.items_seen)} itens`,
        `${num(ativa.items_new)} novos`,
      ];
      if (velocidade > 0) partes.push(`~${num(velocidade)}/s`);
      texto.innerHTML = partes.join(' · ');

      // Enquanto uma sincronização roda, a tela de fontes fica desatualizada em segundos.
      if (!anterior && estado.rota === 'fontes' && !estado.ocupado) navegar();
    } catch {
      // Uma falha de consulta não deve derrubar o acompanhamento.
    }
  }

  verificar();
  setInterval(verificar, 3000);
}

// Usada pelo botão do modal de URL de origem.
window.fecharModal = fecharModal;

// ---------------------------------------------------------------------------
// Partida
// ---------------------------------------------------------------------------

(async function iniciar() {
  try {
    const me = await api('/auth/me');
    estado.usuario = me.user;
    mostrarApp();
    navegar();
  } catch {
    mostrarLogin();
  }
  iniciarBarraSincronizacao();
})();

/**
 * O cartão que responde à pergunta "por que só a fonte X está sendo copiada?".
 *
 * A resposta quase sempre é a mesma: só ela está marcada. Nada proíbe as outras — a marca é
 * por fonte, foi decidida uma vez, e meses depois ninguém lembra quais ficaram marcadas.
 * Antes disso a única forma de descobrir era abrir fonte por fonte.
 *
 * A ORDEM não se edita aqui de propósito. Ela é a prioridade de reprodução, e vale para
 * tudo — não só para o cache. Ter dois lugares onde se arrasta a mesma lista seria um
 * convite a que os dois discordassem.
 */
function cartaoDeFontesDoCache(fontes) {
  // Array.isArray, e nao `fontes || []`: a resposta de /sources vem embrulhada num objeto, e
  // um objeto e tao verdadeiro quanto uma lista. A versao anterior derrubava a tela inteira
  // do Acervo com "filter is not a function" — um cartao informativo nao pode fazer isso.
  const lista = (Array.isArray(fontes) ? fontes : []).filter(f => f.kind !== "proprio");
  if (!lista.length) return '';

  const ordenadas = [...lista].sort((a, b) => (a.priority - b.priority) || a.name.localeCompare(b.name));
  const marcadas = ordenadas.filter(f => f.cache_habilitado).length;

  return `
    <div class="cartao">
      <h2>Fontes que alimentam o acervo</h2>
      <p class="discreto" style="margin:0 0 12px">
        ${marcadas === 0
          ? '<b>Nenhuma fonte está marcada.</b> Nada será copiado, mesmo com o armazenamento ligado.'
          : `<b>${marcadas} de ${ordenadas.length}</b> podem ser copiadas. As demais são sempre
             buscadas na fonte a cada reprodução.`}
        <br>
        A ordem abaixo é a prioridade de reprodução: a primeira disponível é a que toca — e,
        por consequência, a que é guardada. Para mudá-la, arraste em <b>Fontes</b>.
      </p>
      <div class="tabela-wrap"><table>
        <thead><tr>
          <th style="width:1%">#</th><th>Fonte</th><th style="width:1%">Copiar para o acervo</th>
        </tr></thead>
        <tbody>${ordenadas.map((f, i) => `
          <tr>
            <td class="discreto">${i + 1}</td>
            <td>
              <b>${esc(f.name)}</b>
              ${f.enabled ? '' : ' <span class="etiqueta neutro">desativada</span>'}
            </td>
            <td>
              <input type="checkbox" data-cache-fonte="${f.id}"
                     ${f.cache_habilitado ? "checked" : ""} ${f.enabled ? "" : "disabled"}>
            </td>
          </tr>`).join('')}
        </tbody>
      </table></div>
    </div>`;
}

/**
 * O resumo por causa, no topo das Falhas.
 *
 * Existe para responder a pergunta que a lista crua não responde: qual é o motivo
 * PREDOMINANTE. Cem linhas em ordem cronológica embaralham trinta falhas de fonte com duas
 * de disco, e as duas pedem providências opostas.
 *
 * O veredito no fim é a parte que mais importa. Quando a maioria das falhas é da fonte, não
 * há ajuste no sistema que as remova — e saber disso evita procurar defeito onde não há.
 */
function resumoDeFalhas(resumo) {
  if (!Array.isArray(resumo) || !resumo.length) {
    return `<div class="cartao"><div class="veredito ok" style="margin:0">
      <b>Nenhuma falha nas últimas 24 horas.</b>
      Toda reprodução que começou, entregou vídeo.
    </div></div>`;
  }

  const total = resumo.reduce((s, c) => s + c.vezes, 0);
  const deFonte = resumo
    .filter(c => (CAUSAS[c.codigo] || {}).culpa === 'fonte')
    .reduce((s, c) => s + c.vezes, 0);
  const pctFonte = Math.round((deFonte / total) * 100);

  const etiquetaCulpa = {
    fonte: ['alerta', 'da fonte'],
    nosso: ['erro', 'do sistema'],
    cliente: ['neutro', 'do espectador'],
  };

  return `
    <div class="cartao">
      <h2>Por que falhou — últimas 24 horas</h2>
      <div class="tabela-wrap"><table>
        <thead><tr>
          <th>Causa</th><th style="width:1%">Origem</th>
          <th class="numero">Vezes</th><th class="numero">Fontes</th>
        </tr></thead>
        <tbody>${resumo.map(c => {
          const info = CAUSAS[c.codigo] || {};
          const [classe, rotulo] = etiquetaCulpa[info.culpa] || ['neutro', '—'];
          return `<tr>
            <td>
              <b>${esc(info.rotulo || c.codigo)}</b>
              ${info.detalhe ? `<div class="dica">${esc(info.detalhe)}</div>` : ''}
            </td>
            <td><span class="etiqueta ${classe}">${rotulo}</span></td>
            <td class="numero">${num(c.vezes)}</td>
            <td class="numero">${num(c.fontes)}</td>
          </tr>`;
        }).join('')}</tbody>
      </table></div>

      <div class="veredito ${pctFonte >= 70 ? 'alerta' : 'info'}" style="margin:12px 0 0">
        ${pctFonte >= 70
          ? `<b>${pctFonte}% das falhas vieram das fontes</b>, não do sistema. Nenhum ajuste
             aqui as remove — quem cortou a entrega foi o fornecedor.
             <br><br>
             O que muda esse número é o <b>cache</b>: um arquivo guardado no seu disco não
             pode ser cortado no meio, e não depende da fonte estar de pé na hora.`
          : `<b>${100 - pctFonte}% das falhas são do sistema ou de sessões abandonadas.</b>
             Vale olhar o registro do serviço — isto não se explica pela fonte.`}
      </div>
    </div>`;
}

/**
 * Por que a limpeza não está liberando espaço.
 *
 * Só aparece com o armazenamento apertado — com disco sobrando, a limpeza não roda mesmo, e
 * dizer isso seria ruído.
 *
 * A pergunta "está cheio e nada é apagado" tem quatro respostas com ações opostas, e de fora
 * elas são indistinguíveis. Esse silêncio já custou tempo neste sistema mais de uma vez: a
 * fila sem baixador, o "removendo" sem removedor. Aqui a tela diz qual é.
 */
function avisoDaLimpeza(resumo) {
  const esp = resumo.espaco_local;
  const d = resumo.limpeza;
  if (!esp || !esp.apertado || !d) return '';

  const horas = resumo.carencia_horas != null ? resumo.carencia_horas : 24;

  if (d.candidatos > 0) {
    return `<div class="cartao"><div class="veredito info" style="margin:0">
      <b>Armazenamento apertado — a limpeza está trabalhando.</b>
      Há <b>${num(d.candidatos)}</b> cópia(s) que ela pode apagar, começando pelas menos
      usadas. Isso acontece em segundo plano, algumas por minuto.
    </div></div>`;
  }

  if (d.segurados_pela_carencia > 0) {
    return `<div class="cartao"><div class="veredito alerta" style="margin:0">
      <b>A limpeza não tem o que apagar: a carência está segurando tudo.</b>
      <br><br>
      Há <b>${num(d.segurados_pela_carencia)}</b> cópia(s) de cache no disco, mas todas têm
      menos de <b>${num(horas)}h</b> — e a carência as protege para o cache não entrar em
      vaivém.
      <br><br>
      Com um disco pequeno, ${num(horas)}h é tempo demais: ele enche em horas, não em dias.
      Diminua a carência em <b>Configurações → Armazenamento de mídia</b> — algo entre
      <b>2h e 6h</b> costuma ser o certo aqui. Enquanto isso, o sistema simplesmente para de
      guardar e continua servindo da fonte.
    </div></div>`;
  }

  const so = [];
  if (d.proprios > 0) so.push(`<b>${num(d.proprios)}</b> do seu acervo próprio`);
  if (d.protegidos > 0) so.push(`<b>${num(d.protegidos)}</b> protegida(s)`);

  return `<div class="cartao"><div class="veredito alerta" style="margin:0">
    <b>A limpeza não tem o que apagar.</b>
    ${so.length
      ? `O que resta no disco é ${so.join(' e ')} — e a limpeza automática nunca toca nesses.
         Apagar é decisão sua, aqui embaixo.`
      : 'Não há cópias de cache no disco. O espaço está sendo usado por outra coisa nesta máquina.'}
  </div></div>`;
}

/**
 * O espaço de uma conta de nuvem, com a diferença entre três estados que já foram um só.
 *
 * A versão anterior perguntava `if (n.bytes_totais && ...)` e mandava tudo o mais para
 * "ainda não medido". Isso confundia duas coisas bem diferentes:
 *
 *   - conta SEM LIMITE, que o sistema guarda como total zero — e zero é falso em JavaScript,
 *     então uma conta medida com sucesso aparecia como não medida;
 *   - medição que FALHOU, cujo motivo estava guardado e não era mostrado nesta coluna.
 *
 * `medida_em` é o campo que responde "foi medida?", e é ele que decide aqui. Os bytes
 * respondem outra pergunta.
 */
function espacoDaNuvem(n) {
  if (!n.medida_em) {
    return n.ultimo_erro
      ? '<span class="discreto" style="color:var(--erro)">a medição falhou</span>'
      : '<span class="discreto">medindo…</span>';
  }
  if (!n.bytes_totais) {
    return `<b>sem limite</b><div class="dica">${formatarBytes(n.bytes_usados || 0)} em uso</div>`;
  }
  const livre = Math.max(0, n.bytes_totais - (n.bytes_usados || 0));
  const pct = Math.round(((n.bytes_usados || 0) / n.bytes_totais) * 100);
  return `<b>${formatarBytes(livre)} livres</b>
    <div class="dica">${formatarBytes(n.bytes_usados || 0)} de ${formatarBytes(n.bytes_totais)} · ${pct}%</div>`;
}

/**
 * O motivo da última falha, pintado conforme o sistema já desistiu ou não.
 *
 * Antes, qualquer texto de erro saía em vermelho — e isso juntava duas situações opostas:
 *
 *   - "falhou e VAI TENTAR DE NOVO", que é o sistema trabalhando e não pede nada de ninguém;
 *   - "falhou e DESISTIU", que é a única que precisa de decisão humana.
 *
 * A lista ficava com dezenas de linhas vermelhas de coisas que iam se resolver sozinhas, e a
 * conclusão natural — a que o usuário teve — era que estava tudo quebrado. O sinal perdia o
 * valor justamente por ser dado demais.
 */
function motivoDoArquivo(a) {
  if (!a.erro) return '';
  if (a.estado === 'erro') {
    return `<div class="dica" style="color:var(--erro)">${esc(a.erro)}</div>`;
  }
  // Ainda na fila: o texto é histórico, não um chamado.
  const quantas = a.tentativas
    ? ` <span class="discreto">(tentativa ${a.tentativas + 1})</span>`
    : '';
  return `<div class="dica">
    <b>Vai tentar de novo${quantas}:</b> ${esc(a.erro)}
  </div>`;
}
