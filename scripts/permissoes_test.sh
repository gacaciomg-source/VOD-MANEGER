#!/bin/bash
#
# Confere as permissões da instalação lendo os próprios scripts.
#
# Existe por causa de um erro concreto: uma edição automática criou
# /opt/vodmanager/runtime mas perdeu o `chown` no caminho. Os scripts continuaram com
# sintaxe válida, o instalador continuou terminando com sucesso, e a única forma de
# perceber era um "erro interno" no painel dias depois — a mesma falha que a mudança
# dizia estar corrigindo.
#
# São duas travas independentes, e abrir só uma não adianta:
#   - ReadWritePaths, do systemd;
#   - o dono da pasta, do sistema de arquivos.
#
# Uso:  bash scripts/permissoes_test.sh
#
set -uo pipefail

RAIZ=$(cd "$(dirname "$0")/.." && pwd)
FALHAS=0

ok()    { printf '  \033[32mok\033[0m   %s\n' "$1"; }
falha() { printf '  \033[31mFALHA\033[0m %s\n' "$1"; FALHAS=$((FALHAS + 1)); }

exige() {
    local arquivo="$1" padrao="$2" descricao="$3"
    grep -qE "$padrao" "$RAIZ/$arquivo" \
        && ok "$descricao" \
        || falha "$descricao  ($arquivo)"
}

proibe() {
    local arquivo="$1" padrao="$2" descricao="$3"
    grep -qE "$padrao" "$RAIZ/$arquivo" \
        && falha "$descricao  ($arquivo)" \
        || ok "$descricao"
}

echo
echo "Permissões da instalação"

exige scripts/instalar.sh \
    '^mkdir -p /opt/vodmanager/runtime' \
    "o instalador cria a pasta de trabalho"

exige scripts/instalar.sh \
    '^chown vodmanager:vodmanager /opt/vodmanager/runtime' \
    "a pasta de trabalho pertence ao serviço"

exige scripts/instalar.sh \
    '^ReadWritePaths=/opt/vodmanager/runtime' \
    "a unidade libera escrita só na pasta de trabalho"

# ReadWritePaths na pasta inteira devolveria ao processo o poder de trocar o próprio
# binário — exatamente o que a separação em runtime/ existe para impedir.
proibe scripts/instalar.sh \
    '^ReadWritePaths=/opt/vodmanager$' \
    "a unidade NÃO libera escrita na pasta do binário"

# O binário é do root: o serviço só precisa executá-lo.
proibe scripts/instalar.sh \
    'install -o vodmanager' \
    "o instalador grava o binário como root"
proibe scripts/atualizar.sh \
    'install -o vodmanager' \
    "o atualizador grava o binário como root"

echo
echo "Os pedidos e registros ficam na pasta gravável"

for alvo in solicitar-atualizacao ultima-atualizacao.log solicitar-dominio ultimo-dominio.log; do
    # Qualquer caminho /opt/vodmanager/<alvo> sem o runtime/ no meio é escrita numa pasta
    # que o serviço não pode tocar.
    if grep -rn "/opt/vodmanager/$alvo" "$RAIZ/internal" "$RAIZ/scripts" 2>/dev/null | grep -q .; then
        falha "$alvo está fora de runtime/"
    else
        ok "$alvo está em runtime/"
    fi
done

echo
if [ "$FALHAS" -eq 0 ]; then
    printf '\033[32mTodas as verificações passaram.\033[0m\n\n'
    exit 0
fi
printf '\033[31m%d verificação(ões) falharam.\033[0m\n\n' "$FALHAS"
exit 1
