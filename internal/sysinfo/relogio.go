package sysinfo

import "time"

// agora existe para as implementações por sistema operacional compartilharem a mesma
// fonte de tempo, e para o teste poder fixá-la.
var agora = time.Now
