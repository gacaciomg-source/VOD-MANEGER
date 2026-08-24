package api

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"vodmanager/internal/armazenamento"
	"vodmanager/internal/ingest"
	"vodmanager/internal/store"
)

// Envio de arquivo pelo painel.
//
// # O que faz este endpoint ser diferente de todos os outros
//
// Ele recebe gigabytes. Todo o resto da API troca objetos JSON de alguns kilobytes, com um
// limite de 1 MiB no corpo — aqui esse limite não pode existir, e nada pode ser carregado
// em memória.
//
// Por isso o corpo é lido em STREAMING, direto para o armazenamento: os bytes passam por um
// buffer de algumas centenas de kilobytes e vão embora. Um envio de 40 GB custa o mesmo em
// memória que um de 40 MB.
//
// A biblioteca padrão facilitaria o caminho errado. `r.ParseMultipartForm` guarda o arquivo
// inteiro — em memória até um limite, e em disco temporário depois. Num servidor que já usa
// o disco para servir vídeo, isso significaria escrever o arquivo duas vezes e precisar do
// dobro do espaço. `r.MultipartReader` entrega as partes conforme chegam, e é ele que
// permite mandar direto para a nuvem sem tocar no disco local.

// extensoesDeVideo são os contêineres que o sistema entrega.
//
// A lista existe para recusar cedo o que não é vídeo. Não é segurança — quem envia é
// administrador, e ele pode enviar o que quiser — é evitar que um PDF enviado por engano
// vire um item do catálogo que ninguém consegue assistir.
var extensoesDeVideo = map[string]bool{
	"mp4": true, "mkv": true, "avi": true, "mov": true,
	"ts": true, "m4v": true, "webm": true, "mpg": true, "mpeg": true,
}

// handleEnviarArquivo recebe um vídeo e o registra no catálogo.
//
// O formulário traz, nesta ordem: os campos de texto e, por último, o arquivo. A ordem
// importa — os campos são lidos conforme chegam, e um título que viesse DEPOIS do arquivo
// só seria conhecido quando os gigabytes já tivessem sido gravados.
func (s *Server) handleEnviarArquivo(w http.ResponseWriter, r *http.Request) {
	if s.deps.Armazenamento == nil {
		writeError(w, s.deps.Log, http.StatusServiceUnavailable, "sem_armazenamento",
			"este processo não guarda mídia")
		return
	}

	partes, err := r.MultipartReader()
	if err != nil {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"envie o arquivo como multipart/form-data")
		return
	}

	campos := map[string]string{}
	for {
		parte, err := partes.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
				"corpo inválido: "+err.Error())
			return
		}

		if parte.FileName() == "" {
			// Campo de texto. O limite é pequeno de propósito: são títulos e números, e
			// um campo de texto sem teto num endpoint de upload é um jeito fácil de
			// consumir memória.
			valor, err := io.ReadAll(io.LimitReader(parte, 4<<10))
			parte.Close()
			if err != nil {
				writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
					"campo inválido: "+err.Error())
				return
			}
			campos[parte.FormName()] = strings.TrimSpace(string(valor))
			continue
		}

		// A partir daqui é o arquivo, e ele é a última parte.
		s.receberVideo(w, r, parte, campos)
		return
	}

	writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
		"nenhum arquivo foi enviado")
}

func (s *Server) receberVideo(w http.ResponseWriter, r *http.Request, parte *multipart.Part, campos map[string]string) {
	defer parte.Close()

	titulo := campos["titulo"]
	if titulo == "" {
		// Sem título, o nome do arquivo é o melhor palpite — e é melhor que recusar o
		// envio de quem esqueceu de preencher um campo depois de subir 20 GB.
		titulo = strings.TrimSuffix(parte.FileName(), filepath.Ext(parte.FileName()))
	}
	if titulo == "" {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"informe o título", "titulo")
		return
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(parte.FileName()), "."))
	if !extensoesDeVideo[ext] {
		writeError(w, s.deps.Log, http.StatusBadRequest, "invalid_body",
			"o arquivo precisa ser um vídeo (mp4, mkv, avi, mov, ts, m4v, webm, mpg)",
			"arquivo")
		return
	}

	// Onde guardar: o que o formulário pedir, ou o destino padrão das configurações.
	destino := campos["backend"]
	if destino == "" {
		destino, _ = s.deps.Store.GetSetting(r.Context(), store.SettingCacheBackend, store.BackendLocal)
	}
	if destino != store.BackendLocal && destino != store.BackendNuvem {
		destino = store.BackendLocal
	}

	chave := armazenamento.ChaveLocal
	var nuvemID *int64
	if destino == store.BackendNuvem {
		nuvem, err := s.deps.Store.NuvemParaGravar(r.Context(), 0)
		if err != nil {
			writeError(w, s.deps.Log, http.StatusConflict, "sem_nuvem",
				"nenhuma conta de nuvem pode receber agora. Cadastre uma no Acervo, ou "+
					"escolha o disco desta máquina como destino.")
			return
		}
		nuvemID = &nuvem.ID
		chave = armazenamento.ChaveDaNuvem(nuvem.ID)
	}

	backend, ok := s.deps.Armazenamento.Obter(chave)
	if !ok {
		writeError(w, s.deps.Log, http.StatusServiceUnavailable, "sem_armazenamento",
			"o destino escolhido não está disponível nesta máquina")
		return
	}

	// A gravação vem ANTES do registro no catálogo.
	//
	// O contrário deixaria, a cada falha de rede no meio de um envio de 20 GB, um filme no
	// catálogo que não abre — e o cliente descobriria apertando o play. Gravando primeiro,
	// uma falha não deixa rastro no catálogo: o que sobra é um arquivo órfão, que a tela do
	// Acervo mostra e você apaga.
	local, err := backend.Guardar(r.Context(), parte.FileName(), parte, 0)
	if err != nil {
		if errors.Is(err, armazenamento.ErrSemEspaco) {
			writeError(w, s.deps.Log, http.StatusInsufficientStorage, "sem_espaco",
				"não há espaço no destino escolhido")
			return
		}
		s.fail(w, r, err, "gravando o arquivo enviado")
		return
	}

	fonte, err := s.deps.Store.FonteDoAcervo(r.Context())
	if err != nil {
		_ = backend.Apagar(r.Context(), local.Localizador)
		s.fail(w, r, err, "obtendo a fonte do acervo")
		return
	}

	conteudo := store.ConteudoProprio{
		Titulo:            titulo,
		TituloNormalizado: ingest.NormalizeName(titulo),
		Tipo:              store.ContentMovie,
		ContainerExt:      ext,
		Backend:           destino,
		NuvemID:           nuvemID,
		Bytes:             local.Bytes,
		Localizador:       local.Localizador,
	}
	if v := campos["ano"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 1800 && n < 2200 {
			conteudo.Ano = &n
		}
	}
	if v := campos["categoria_id"]; v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			conteudo.CategoriaID = &n
		}
	}

	res, err := s.deps.Store.CriarConteudoProprio(r.Context(), fonte.ID, conteudo)
	if err != nil {
		// O arquivo já está gravado e o catálogo não o conhece: apagar evita ocupar
		// espaço que ninguém mais sabe que está ocupado.
		_ = backend.Apagar(r.Context(), local.Localizador)
		s.fail(w, r, err, "registrando o conteúdo enviado")
		return
	}

	s.logEvent(r, "acervo", "info",
		"arquivo enviado pelo painel: "+titulo, actorOf(r), nil)
	writeJSON(w, s.deps.Log, http.StatusCreated, map[string]any{
		"content_id": res.ContentID,
		"arquivo_id": res.ArquivoID,
		"titulo":     titulo,
		"bytes":      local.Bytes,
		"destino":    destino,
		"aviso": "O arquivo está no acervo e já aparece no catálogo. Ele é ACERVO PRÓPRIO: " +
			"a limpeza automática nunca o apaga, nem com o disco cheio.",
	})
}
