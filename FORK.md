# Fork Kırpma Listesi (server-only, stock client uyumlu)

Kaynak: DemirSe/xray-optimized-core (parent XTLS/Xray-core, pin v26.3.27).
Hedef dosya: `main/distro/all/all.go` import listesi + `go.mod` budaması.
Kural: config şeması ve Reality handshake BİREBİR aynı kalır (client stock).

## TUT (zorunlu)

- `app/dispatcher`, `app/proxyman/inbound`, `app/proxyman/outbound` (çekirdek)
- `app/log`, `app/policy` (dispatcher/vless/freedom kullanıyor — grep ile doğrulandı)
- `app/reverse` (vless inbound importluyor — doğrulandı)
- `proxy/vless/inbound` (SADECE inbound; outbound paketi ayrı, atılıyor)
- `proxy/freedom`
- `transport/internet`: `tcp`, `udp` (freedom UDP dial-out için), `reality`, `tls` (reality importluyor — doğrulandı)
- `transport/internet/headers/noop`, `transport/internet/tagged/taggedimpl` (döngü fix'i)
- `main/json` (tek config formatı), dosya confloader (http `external` değil)
- Komutlar: `tls` (ping), `uuid`, `x25519` + base (api/convert/wg/vlessenc atılır)

## AT (güvenli, configimiz kullanmıyor)

- Proxy: `blackhole`, `dns`, `dokodemo`, `http`, `loopback`, `shadowsocks`, `socks`,
  `trojan`, `vless/outbound`, `vmess/inbound`, `vmess/outbound`, `wireguard`
  (tun/hysteria/shadowsocks_2022 zaten all.go'da yok)
- Transport: `grpc`, `httpupgrade`, `kcp`, `splithttp`, `websocket`, `headers/http`
  (browser_dialer bunlarla gidiyor — doğrulandı)
- App: `commander` + `log/proxyman/stats/observatory` command'leri, `dns`, `fakedns`,
  `geodata` (app), `metrics`, `observatory`, `reverse` DIŞINDAKİLER, `router` (config'de
  routing yok, infra routersuz çalışıyor — build ile doğrulanacak), `stats`
- `main/toml`, `main/yaml`, `main/confloader/external`, `main/commands/all/{api,convert}`
- go.mod'dan düşenler: grpc, quic, wireguard-go, shadowsocks libleri, yaml/toml libleri

## RİSKLİ (build+rig test ister)

- `router`: config'de routing bloğu yok; dispatcher routersuz default-outbound'a gider
  (önceki minimal config bilgisi + infra kodu). Build sonrası rig'de trafik testi şart.
- `tls` transport paketi: config'de geçmiyor ama reality importluyor → TUTULDU (güvenli taraf).

## Build planı

1. Fork'ta `trimmed` branch: all.go importları yukarıdaki gibi + `go mod tidy`.
2. GitHub Actions: Debian amd64, `-trimpath -ldflags="-s -w"`, artifact + sha256.
3. Kabul: `xray run -test` canlı config ile OK + rig'de (:1443) 5×2MB + 10KB medyanları
   stock'un ±%10 bandında + `xray tls ping whatsapp.net:443` çalışıyor.
4. Canlıya alma: yedek + tek-komut rollback (ayrı y/n).

## Dersler (canlı kanıtlı)

- `main/confloader/external` SADECE http değil, DOSYA okuyucuyu da kurar. Atınca binary
  hiç config okuyamıyor (stdin EOF). Geri eklendi.
- `all.go` import silmek YETMEZ: `infra/conf` parser'ları her protokol paketini zaten
  çekiyor (ölçüldü: binary -115KB, vmess hâlâ çalışıyordu). Gerçek budama =
  klasör + parser silmek (400 dosya, -64k satır).
- `reality` paketi `tls` transportunu importluyor, `vless/inbound` `app/reverse` istiyor —
  ikisi de tutuldu (grep doğrulamalı).
- go.mod budanmadı: silinen paketlere test dosyaları referans veriyor; binary'e etkisi
  yok (linker ölü kodu atıyor), CI indirmesi biraz şişkin. Bilinçli erteleme.

## Sonuç (2026-09-05, local build)

- 32.4MB → 23.8MB (-%26). CLI: run/version/tls/uuid/x25519 (api/convert/wg gitti).
- Reality config OK; vmess/socks config RED (unknown config id).
- Canlı trafik: trimmed-server + stock-client, gerçek whatsapp.net dest, HTTP 200 doğru içerik.
- CI `trimmed-build` yeşil (build + uuid + config OK/RED + sha256 + artifact).
- Upstream workflow'lar fork'ta kapatıldı (sadece trimmed-build).
- SIRADAKİ: rig A/B (:1443, server) + rollout — server'a dokunur, ayrı y/n gerekir.

## Rollout (2026-09-05) — CANLIYA ALINDI

- A/B: 2MB stock ~1.26s / fork ~1.19s (eşit, kabul). 10KB yük-altında stock 4/4
  bozuldu (2–3s stall), fork 4/4 stabil ~0.3s. RSS fork 27MB / stock 38MB.
  Mekanizma bilinmiyor (server işleme anında, stall altta yatan TCP'de) — muhafazakar
  hüküm: fork stock'tan kötü değil, yükte daha stabil göründü.
- Live `:443` fork binary (1374f79) + Meta forward doğrulandı.
- Rollback: `systemctl stop xray; cp /usr/local/bin/xray.stock-26.3.27 /usr/local/bin/xray; systemctl start xray`.

## Beklenen kazanç (ölçülmeden iddia yok)

- Binary küçülür + tedarik-zinciri daralır (az bağımlılık = az CVE yüzeyi).
- Hız artışı BEKLENMEZ (hot path zaten Vision/splice; RSS/CPU aynı kalırsa başarı).
