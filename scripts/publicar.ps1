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
    [string]$Mensagem
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

git push
if (-not $?) {
    Erro "o push falhou. O commit está feito localmente — corrija e rode 'git push'."
    Write-Host "`nSe o remoto tiver trabalho que você não tem:" -ForegroundColor Yellow
    Write-Host "    git pull --rebase" -ForegroundColor Yellow
    Write-Host "    git push" -ForegroundColor Yellow
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
