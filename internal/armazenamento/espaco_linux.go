//go:build linux

package armazenamento

import (
	"context"
	"fmt"
	"syscall"
)

// Espaco lê o sistema de arquivos onde a pasta está.
//
// Usa Bavail, e não Bfree: o kernel reserva uma fatia para o root, e Bfree a inclui. Contar
// com ela é prometer espaço que um processo comum não consegue usar — a gravação falharia
// com "disco cheio" num disco que o painel acabou de dizer ter espaço.
func (l *Local) Espaco(_ context.Context) (Espaco, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(l.raiz, &fs); err != nil {
		return Espaco{}, fmt.Errorf("armazenamento local: medindo %s: %w", l.raiz, err)
	}
	bloco := int64(fs.Bsize)
	total := int64(fs.Blocks) * bloco
	livre := int64(fs.Bavail) * bloco

	// A reserva sai do que anunciamos como livre. Assim todo o resto do sistema — a
	// decisão de guardar, a tela, a limpeza — trabalha com o número já descontado, e
	// ninguém precisa lembrar de subtrair.
	livre -= l.reservaBytes
	if livre < 0 {
		livre = 0
	}
	return Espaco{Total: total, Livre: livre, Usado: total - livre}, nil
}
