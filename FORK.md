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

## Beklenen kazanç (ölçülmeden iddia yok)

- Binary küçülür + tedarik-zinciri daralır (az bağımlılık = az CVE yüzeyi).
- Hız artışı BEKLENMEZ (hot path zaten Vision/splice; RSS/CPU aynı kalırsa başarı).
