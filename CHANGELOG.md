# Changelog

## 1.5.0 (2026-06-24)

### Features
- **Audit log**: `GET /api/audit` отдаёт последние 100 запросов (timestamp, IP, method, path, status)
- **Metrics endpoint**: `GET /api/metrics` — метрики контейнеров, образов, CPU/memory/disk
- **Graceful shutdown**: таймаут увеличен до 10s, корректное завершение WebSocket консолей
- **Config**: добавлен параметр `audit_size` для лимита записей аудита

### Code Quality
- **Server tests**: 18 unit-тестов (health, rate limiter, validation, method checks, body limits)

### Bug Fixes
- **Container ID validation**: `/` больше не блокирует action-запросы (start/stop/restart)

## 1.3.0 (2026-06-13)

### Security
- **Shell injection fix**: fileWrite/fileUpload используют `cat > "$1"` с аргументом вместо подстановки в shell
- **Path traversal fix**: `filepath.Clean` + prefix check для всех path параметров
- **TLS support**: опционально `tls_cert`/`tls_key` в конфиге
- **Rate limiting**: 100 req/sec per IP token bucket в auth middleware

### Bug Fixes
- **TOML парсер**: заменён самодельный парсер на `github.com/BurntSushi/toml`
- **Image pull handler**: больше не смешивает stream + JSON в одном HTTP ответе
- **Body size limits**: `http.MaxBytesReader` (1MB для JSON, 10MB для file uploads)
- **LogDir**: теперь реально пишет логи в указанную директорию
- **Timeout**: `dck_timeout` в конфиге вместо хардкода 30s

## 1.2.0 (Previous)
- Enhanced health endpoint with system stats
- Add disk param to create container endpoint
- startup_script support
