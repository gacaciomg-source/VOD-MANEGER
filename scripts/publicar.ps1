# Publica as alterações no GitHub.
#
#   .\scripts\publicar.ps1
#   .\scripts\publicar.ps1 "mensagem do commit"
#
# Existe por dois motivos concretos:
#
#  1. O Windows PowerShell 5.1 não aceita `&&`. Uma sequência como
#     `git commit -m "..." && git push` falha DEPOIS do commit — o commit acontece, o push
#     não, e nada avisa. Foi assim que uma publicação ficou pela metade sem ninguém notar.
#
#  2. A conferência de segredos precisa acontecer ANTES do commit, sempre. A chave de
#     criptografia num repositório, junto de um backup, entrega as credenciais das fontes e
#     as senhas dos clientes. Depender de lembrar de conferir é depender de nunca esquecer.

param(
    [Parameter(Position = 0)]
    [string]$Mensagem,

    # Usado apenas quando o repositorio local ainda nao tem remoto configurado.
    [string]$RepositorioPadrao = "https://github.com/gacaciomg-source/VOD-MANEGER.git"
)

$ErrorActionPreference = "Stop"

function Passo($texto) { Write-Host "`n==> $texto" -ForegroundColor Cyan }
function Ok($texto)    { Write-Host "    $texto" -ForegroundColor Green }
function Erro($texto)  { Write-Host "`nerro: $texto" -ForegroundColor Red }

# Roda sempre a partir da raiz do projeto, não de onde o usuário chamou.
$raiz = Split-Path -Parent $PSScriptRoot
Set-Location $raiz

if (-not (Test-Path "go.mod")) {
    Erro "não encontrei go.mod em $raiz. Este script vive em scripts/ dentro do projeto."
    exit 1
}

# ---------------------------------------------------------------------------
Passo "1/5  Preparando os arquivos"

git add -A
if (-not $?) { Erro "git add falhou."; exit 1 }

$alterados = git diff --cached --name-only
if (-not $alterados) {
    Write-Host "`nNada a publicar: nenhuma alteração pendente." -ForegroundColor Yellow
    exit 0
}
Ok "$($alterados.Count) arquivo(s)"

# ---------------------------------------------------------------------------
Passo "2/5  Conferindo se nenhum segredo entrou"

# A lista é do que NUNCA pode ir para o repositório, nem privado. Cada padrão corresponde a
# algo que, sozinho ou com um backup, dá acesso ao sistema de alguém.
$padrao = '(^|/)\.env$|\.key$|(^|/)\.vodm-dev/|encryption|cookies\.txt|\.pem$|\.tar\.gz$'
$suspeitos = git ls-files | Select-String -Pattern $padrao

if ($suspeitos) {
    Erro "há arquivos sensíveis preparados para o commit:"
    $suspeitos | ForEach-Object { Write-Host "    $_" -ForegroundColor Red }
    Write-Host "`nNada foi publicado. Remova-os do commit antes de continuar:" -ForegroundColor Yellow
    Write-Host "    git reset ARQUIVO" -ForegroundColor Yellow
    exit 1
}
Ok "nenhum segredo no commit"

# ---------------------------------------------------------------------------
Passo "3/5  O que vai junto"

# O painel é servido de dentro do binário. Esquecê-lo publica um servidor com telas novas
# no back-end e nenhuma tela para chegar até elas — foi exatamente o que aconteceu uma vez.
$painel = $alterados | Where-Object { $_ -like "internal/panel/*" }
if ($painel) {
    Ok "painel incluído ($($painel.Count) arquivo(s))"
}

$alterados | Select-Object -First 15 | ForEach-Object { Write-Host "    $_" }
if ($alterados.Count -gt 15) {
    Write-Host "    ... e mais $($alterados.Count - 15)"
}

# ---------------------------------------------------------------------------
Passo "4/5  Commit"

if (-not $Mensagem) {
    $Mensagem = "atualizacao " + (Get-Date -Format "yyyy-MM-dd HH:mm")
}
git commit -m $Mensagem
if (-not $?) { Erro "o commit falhou."; exit 1 }
Ok $Mensagem

# ---------------------------------------------------------------------------
Passo "5/5  Enviando ao GitHub"

# Sem remoto configurado, o push falha com uma mensagem longa do git que não diz o que
# fazer neste projeto. Configuramos na hora, com o endereço conhecido.
$temRemoto = git remote 2>$null
if (-not $temRemoto) {
    Write-Host "    remoto nao configurado; apontando para o repositorio do projeto" -ForegroundColor Yellow
    git remote add origin $RepositorioPadrao
}

# -u na primeira vez liga o ramo local ao remoto; nas seguintes o git ja sabe o destino.
$ramo = (git rev-parse --abbrev-ref HEAD)
git push -u origin $ramo
if (-not $?) {
    Erro "o push foi recusado. O commit esta feito localmente; so o envio falhou."
    Write-Host ""
    Write-Host "Se o remoto tiver trabalho que voce nao tem:" -ForegroundColor Yellow
    Write-Host "    git pull --rebase origin $ramo" -ForegroundColor Yellow
    Write-Host "    git push" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "Se o remoto tiver historico SEM RELACAO com este - o caso de um" -ForegroundColor Yellow
    Write-Host "repositorio criado por upload pela pagina do GitHub - o comando abaixo" -ForegroundColor Yellow
    Write-Host "SUBSTITUI o que esta la pelo que voce tem aqui:" -ForegroundColor Yellow
    Write-Host "    git push --force -u origin $ramo" -ForegroundColor Yellow
    exit 1
}

$commit = (git rev-parse --short HEAD)
Write-Host "`nPublicado. Commit $commit" -ForegroundColor Green
Write-Host ""
Write-Host "Para aplicar na VPS:" -ForegroundColor Cyan
Write-Host "    cd /opt/vodmanager-fonte"
Write-Host "    git pull"
Write-Host "    sudo bash scripts/atualizar.sh"
Write-Host ""
Write-Host "Ou, no painel: Sistema -> Atualizacao."
Write-Host ""
