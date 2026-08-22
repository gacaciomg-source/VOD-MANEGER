//go:build !linux

package armazenamento

import "context"

// Fora do Linux não medimos o sistema de arquivos.
//
// A produção é Ubuntu/Debian; Windows e macOS são ambientes de desenvolvimento. Escrever a
// leitura para eles seria manter código que nunca protege ninguém.
//
// Ilimitado, e não "zero livre": zero faria toda gravação ser recusada por falta de espaço
// na máquina de quem está desenvolvendo, que é o pior jeito possível de sinalizar
// "não sei medir".
func (l *Local) Espaco(_ context.Context) (Espaco, error) {
	return Espaco{Ilimitado: true}, nil
}
