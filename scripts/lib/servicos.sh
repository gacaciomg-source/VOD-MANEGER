#!/bin/bash
#
# Unidades systemd, pasta de trabalho e firewall do VOD Manager — num lugar só.
#
# # Por que este arquivo existe
#
# O instalador e o atualizador precisam produzir EXATAMENTE a mesma instalação. Enquanto
# as unidades viviam só dentro do instalador, atualizar pelo painel trocava o binário mas
# não trazia nada do que a versão nova precisasse do sistema: uma unidade nova, uma pasta
# nova, uma permissão nova. O resultado era o conselho "melhor rodar o instalador de novo"
# — ou seja, um botão de atualizar que não atualizava.
#
# Com as unidades aqui, o atualizador reaplica a mesma configuração que o instalador
# aplicaria, e o botão passa a valer por si.
#
# Todas as funções são IDEMPOTENTES: rodar de novo não estraga nada.
#
# Quem usa este arquivo define antes:
#   FONTE     pasta do código-fonte clonado
#   DESTINO   caminho do binário instalado
#   AMBIENTE  arquivo de variáveis (/etc/vodmanager.env)
#   SERVICO   nome do serviço (vodmanager)

# vodm_pasta_runtime cria a pasta gravável do serviço.
#
# O serviço precisa ESCREVER para pedir atualização, domínio ou migração ao systemd.
# Deixar /opt/vodmanager inteira gravável resolveria — e daria ao processo exposto na
# internet o poder de substituir o próprio binário. Então a escrita fica confinada aqui.
#
# O ReadWritePaths da unidade NÃO basta: ele levanta a proteção do systemd, não a permissão
# do sistema de arquivos. São duas travas, e abrir só uma dá "permission denied".
vodm_pasta_runtime() {
    mkdir -p /opt/vodmanager/runtime
    chown vodmanager:vodmanager /opt/vodmanager/runtime
    chmod 0750 /opt/vodmanager/runtime
}

# vodm_unidades escreve todas as unidades systemd e as habilita.
#
# # O mecanismo dos botões do painel
#
# O serviço roda como o usuário vodmanager, que não pode reiniciar serviços nem escrever
# fora da pasta de trabalho — e roda com NoNewPrivileges, que impede até o sudo de elevar.
# Ambas as restrições são boas e ficam.
#
# Em vez de abrir uma exceção nelas, o sentido é invertido: o painel apenas CRIA UM ARQUIVO
# dentro da pasta onde já pode escrever, e o systemd — que é root — observa esse arquivo e
# dispara o script. O processo nunca ganha privilégio nenhum e não escolhe o que roda: só
# pede que aconteça o que o root já definiu aqui.
vodm_unidades() {
    cat > /etc/systemd/system/${SERVICO}.service <<UNIT
[Unit]
Description=VOD Manager
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=vodmanager
Group=vodmanager
EnvironmentFile=${AMBIENTE}
ExecStart=${DESTINO}
Restart=always
RestartSec=5

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/vodmanager/runtime

# Streaming abre muitos descritores ao mesmo tempo; sem isto o serviço trava com
# "Too many open files" justamente quando houver audiência.
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
UNIT

    # Atualização pelo painel.
    #
    # WorkingDirectory é obrigatório e já foi a causa de o botão falhar: sem ele o systemd
    # roda o script a partir de /, onde não há go.mod, e o atualizador para na primeira
    # verificação com "rode de dentro da pasta do código" — uma mensagem correta e
    # completamente inútil para quem só clicou num botão.
    cat > /etc/systemd/system/vodmanager-update.service <<UNIT
[Unit]
Description=Atualização do VOD Manager (disparada pelo painel)

[Service]
Type=oneshot
WorkingDirectory=${FONTE}
# O pedido é consumido ANTES de começar: se o script falhar, o arquivo já não existe e a
# atualização não fica repetindo em laço.
ExecStartPre=/bin/rm -f /opt/vodmanager/runtime/solicitar-atualizacao
ExecStart=/bin/bash ${FONTE}/scripts/atualizar.sh
# Compilar o projeto inteiro numa VPS pequena passa dos 5 minutos do padrão.
TimeoutStartSec=3600
UNIT

    cat > /etc/systemd/system/vodmanager-update.path <<UNIT
[Unit]
Description=Observa o pedido de atualização vindo do painel

[Path]
PathExists=/opt/vodmanager/runtime/solicitar-atualizacao

[Install]
WantedBy=multi-user.target
UNIT

    # Configuração de domínio pelo painel.
    cat > /etc/systemd/system/vodmanager-domain.service <<UNIT
[Unit]
Description=Configuração de domínio do VOD Manager (disparada pelo painel)

[Service]
Type=oneshot
WorkingDirectory=${FONTE}
# O pedido carrega o domínio e o e-mail, e é consumido ANTES de começar: se o script
# falhar, o arquivo já não existe e a tarefa não fica repetindo em laço.
ExecStart=/bin/bash -c 'read -r d e < /opt/vodmanager/runtime/solicitar-dominio; rm -f /opt/vodmanager/runtime/solicitar-dominio; ${FONTE}/scripts/dominio.sh "\$d" "\$e"'
TimeoutStartSec=1800
UNIT

    cat > /etc/systemd/system/vodmanager-domain.path <<UNIT
[Unit]
Description=Observa o pedido de domínio vindo do painel

[Path]
PathExists=/opt/vodmanager/runtime/solicitar-dominio

[Install]
WantedBy=multi-user.target
UNIT

    # Migração para outra máquina pelo painel.
    #
    # O pedido traz a senha do SSH do destino. Ele é apagado antes de o script começar, e o
    # conteúdo viaja pela entrada padrão — nunca como argumento, que apareceria no `ps`
    # para qualquer usuário da máquina enquanto a migração roda.
    cat > /etc/systemd/system/vodmanager-migrar.service <<UNIT
[Unit]
Description=Migração do VOD Manager para outra máquina (disparada pelo painel)

[Service]
Type=oneshot
WorkingDirectory=${FONTE}
ExecStart=/bin/bash ${FONTE}/scripts/migrar_pedido.sh
# Copiar o catálogo e compilar no destino leva bem mais que o padrão de 5 minutos.
TimeoutStartSec=7200
UNIT

    cat > /etc/systemd/system/vodmanager-migrar.path <<UNIT
[Unit]
Description=Observa o pedido de migração vindo do painel

[Path]
PathExists=/opt/vodmanager/runtime/solicitar-migracao

[Install]
WantedBy=multi-user.target
UNIT

    rm -f /etc/sudoers.d/vodmanager   # de versões anteriores deste instalador
    systemctl daemon-reload
    systemctl enable --now --quiet vodmanager-update.path
    systemctl enable --now --quiet vodmanager-domain.path
    systemctl enable --now --quiet vodmanager-migrar.path
}

# vodm_firewall libera o SSH e a porta da aplicação.
vodm_firewall() {
    local porta="$1"
    ufw allow OpenSSH >/dev/null 2>&1 || true
    ufw allow "${porta}/tcp" >/dev/null 2>&1 || true
    ufw --force enable >/dev/null 2>&1 || true
}

# vodm_pacotes instala apenas o que estiver faltando.
#
# Só chama o apt-get quando há algo a instalar: numa atualização, o caso comum é não faltar
# nada, e um `apt-get update` desnecessário acrescenta um minuto a cada clique no botão.
vodm_pacotes() {
    local faltando=()
    local p
    for p in "$@"; do
        dpkg -s "$p" >/dev/null 2>&1 || faltando+=("$p")
    done
    [ ${#faltando[@]} -eq 0 ] && return 0
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq "${faltando[@]}" >/dev/null
}

# vodm_atualizar_fonte traz a versão nova do repositório para a pasta do código.
#
# Descarta alterações locais de propósito: a pasta do código é uma cópia de trabalho do
# sistema, não um lugar para editar. Um arquivo alterado à mão ali travaria o `pull` e
# faria o botão de atualizar falhar por um motivo que ninguém lembra.
vodm_atualizar_fonte() {
    local pasta="$1" repo="$2"
    if [ ! -d "$pasta/.git" ]; then
        rm -rf "$pasta"
        git clone --quiet "$repo" "$pasta"
        return
    fi
    git -C "$pasta" fetch --quiet --all --tags --prune
    local ramo
    ramo=$(git -C "$pasta" symbolic-ref --quiet --short refs/remotes/origin/HEAD 2>/dev/null || echo "")
    [ -n "$ramo" ] || ramo="origin/main"
    git -C "$pasta" reset --quiet --hard "$ramo"
}
