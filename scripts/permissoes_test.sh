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

exige scripts/lib/servicos.sh \
    '^ *mkdir -p /opt/vodmanager/runtime' \
    "a instalação cria a pasta de trabalho"

exige scripts/lib/servicos.sh \
    '^ *chown vodmanager:vodmanager /opt/vodmanager/runtime' \
    "a pasta de trabalho pertence ao serviço"

exige scripts/lib/servicos.sh \
    '^ReadWritePaths=/opt/vodmanager/runtime' \
    "a unidade libera escrita na pasta de trabalho"

# O acervo guarda vídeo, e precisa das MESMAS duas travas abertas: o dono da pasta e o
# ReadWritePaths. Com ProtectSystem=strict, o dono certo sem a unidade produz um
# "read-only file system" em cima de uma pasta que pertence ao próprio serviço.
exige scripts/lib/servicos.sh \
    '^ReadWritePaths=.*/opt/vodmanager/acervo' \
    "a unidade libera escrita na pasta do acervo"
exige scripts/lib/servicos.sh \
    '^ *chown vodmanager:vodmanager /opt/vodmanager/acervo' \
    "a pasta do acervo pertence ao serviço"

# ReadWritePaths na pasta inteira devolveria ao processo o poder de trocar o próprio
# binário — exatamente o que a separação em runtime/ existe para impedir.
proibe scripts/lib/servicos.sh \
    '^ReadWritePaths=/opt/vodmanager$' \
    "a unidade NÃO libera escrita na pasta do binário"

# O instalador e o atualizador têm de produzir a MESMA instalação. Enquanto as unidades
# viviam dentro do instalador, o botão de atualizar trocava o binário e não trazia nada do
# que a versão nova precisasse do sistema.
exige scripts/instalar.sh '^vodm_unidades$' \
    "o instalador usa as unidades compartilhadas"
exige scripts/atualizar.sh '^vodm_unidades$' \
    "o atualizador reaplica as mesmas unidades"

# A unidade de atualização já rodou sem WorkingDirectory. O systemd executa a partir de /,
# onde não há go.mod, e o botão do painel morria com "rode de dentro da pasta do código" —
# um erro correto e inútil para quem só clicou.
exige scripts/lib/servicos.sh \
    '^WorkingDirectory=\$\{FONTE\}' \
    "as unidades rodam de dentro da pasta do código"

# Sem buscar a versão nova, atualizar recompila exatamente o mesmo código e anuncia
# sucesso sem ter mudado nada — a pior forma de falhar, porque parece a melhor.
exige scripts/atualizar.sh '^ *vodm_atualizar_fonte ' \
    "o atualizador busca a versão nova do repositório"

# O botão atualiza a linha em que a máquina está, e não sempre a principal.
#
# Forçar origin/main arrastava de volta para a principal quem estivesse numa branch para
# testar um recurso — em silêncio, e com a aparência de que o recurso tinha sumido. Um botão
# em que não se confia é um botão que não existe.
exige scripts/lib/servicos.sh \
    'symbolic-ref --quiet --short HEAD' \
    "o atualizador respeita a linha em que a máquina está"

echo
echo "O que o systemd não fornece e os scripts precisam"

# O systemd NÃO define HOME em serviços de sistema. O Go deriva dele o cache de módulos, e
# git, ssh e certbot procuram nele a configuração.
#
# O que torna esta falha traiçoeira é ela ser assimétrica: pelo terminal o sudo define o
# HOME e tudo funciona; pelo BOTÃO do painel não há HOME, e o build morre com "module cache
# not found" — uma mensagem sem relação nenhuma com o projeto, no único caminho que existe
# para não precisar de terminal.
for script in atualizar dominio migrar_pedido; do
    exige "scripts/${script}.sh" '^export HOME="\$\{HOME:-/root\}"' \
        "${script}.sh define HOME quando não há nenhum"
done

# A unidade também define, para o caso de alguém apagar a defesa do script. Cinto e
# suspensório de propósito: a unidade nova só é escrita por uma atualização bem-sucedida,
# então ela sozinha nunca consertaria a atualização que está falhando.
exige scripts/lib/servicos.sh '^Environment=HOME=/root' \
    "as unidades do systemd definem HOME"

# O binário é do root: o serviço só precisa executá-lo.
proibe scripts/instalar.sh \
    'install -o vodmanager' \
    "o instalador grava o binário como root"
proibe scripts/atualizar.sh \
    'install -o vodmanager' \
    "o atualizador grava o binário como root"

echo
echo "Os pedidos e registros ficam na pasta gravável"

for alvo in solicitar-atualizacao ultima-atualizacao.log \
            solicitar-dominio    ultimo-dominio.log \
            solicitar-migracao   ultima-migracao.log; do
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
