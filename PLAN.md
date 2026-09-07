# xray-optimized PLAN

Kural: kritik her bilgi 2 kaynaktan kontrol edilir.

## Setup (netleşti - final)
- Server: Istanbul DC / Netlen, Debian 13, public IP, CGNAT yok, sınırsız erişim, IPv4+IPv6 var
- In: IPv4:443, Out: IPv4/IPv6 (freedom dual-stack)
- Cihaz: max 3 (Android+Win+Linux), tek ortak UUID + tek shortId
- Politika: %100 TUN, bypass YOK, WhatsApp dahil her şey tünel içi
- In: IPv4:443 only (client v4), Out: dual v4/v6 (client v6 out OK)
- Dest/SNI: her zaman yalnızca `whatsapp.net` (`dest=whatsapp.net:443`, `serverNames=[whatsapp.net]`). Başka domain veya alt domaine geçiş YOK; dest fallback YOK (kabul edilen risk: dest bozulursa bağlantı kesilir).
- Fork kapsam: server-only kırpılmış build, client stock Hiddify sabit. Tut: VLESS+Reality+Vision+TCP+UDP+freedom+v4/v6, config şeması aynı. Kırp: VMess/Trojan/SS/gRPC/QUIC/API vb.
- Test: stock-client+stock-server vs stock-client+fork-server, tekrarlı (sayı+metrik netleşecek). VPS-local loopback sadece binary CPU/RAM/throughput için, gerçek ağ testi ayrı.
- Log: optimize bitene kadar açık (warning+access), sonra kapatılacak
- SSH: şifre ile ilk giriş, hemen key'e geçilecek

## Çift-kontrol bulgular
1. REALITY dest kriteri: TLS1.3 + h2, redirect yok, server'a network-yakın IP, ServerHello sonrası şifreli. Kaynak: XTLS REALITY README + Xray-docs-next reality.md
2. `web.whatsapp.com:443`: TLS1.3 var + ALPN h2 var, cert SAN `whatsapp.net + *.whatsapp.net`. Yani dest olarak plausible. Kaynak: testtls.com + WhatsApp FAQ (443). RISK: Meta CDN/anycast, bölgeye göre değişir -> server'dan `xray tls ping` ile teyit şart.
3. Domain gerekmez: REALITY IP + SNI ile çalışır, cert istemez. Kullanıcının "domain gerek yok" iddiası DOĞRU.
4. Client: 3 OS'ta tek aile = Hiddify (sing-box core, VLESS+REALITY+TUN). Alternatif v2rayNG (Android) + Throne/Nekoray (Win/Lin) ama 2 stack = 2x bakım. Az bakım için Hiddify hepsinde.
5. TUN: Android tek VPN izni, Win/Lin admin ister. DNS remote `tcp://1.1.1.1`, Local DNS TUN bozar. UDP, TCP tünel içinde taşınır (oyun/VoIP çalışır, latency artar).

## Plan
0. Sabit dest'i doğrula: `xray tls ping whatsapp.net:443`. Başka dest/SNI seçilmez.
1. Server: resmi XTLS/Xray-install, VLESS `xtls-rprx-vision`, `target=whatsapp.net:443`, `serverNames=[whatsapp.net]`, shortId random 8byte, fp=chrome, 443 TCP dinle, ufw/firewall aç, BBR aç.
2. Tek vless:// link üret (uuid, pbk, sid, sni, fp=chrome, flow=vision).
3. Client 3 OS: Hiddify, TUN ON + System Proxy ON, Strict Route ON, IPv4-only (başta), Remote DNS tcp://1.1.1.1, UDP enabled. WhatsApp bypass YOK; her zaman tünel içinde.
4. Doğrulanan: telefonda VPN bağlantısı ve WhatsApp mesajlaşması. Sesli/görüntülü arama test edilmedi; kullanıcı arama testi yapmayacak. Gerçek hat performans ölçümü ve diğer istemci testleri doğrulanmış sayılmaz.

## Kesinti politikası
- WhatsApp dahil bypass/direct kuralı eklenmez; sorunlar tünel içinde teşhis edilir.
- Dest bozulursa: başka hedefe geçilmez; yalnızca `whatsapp.net` erişimi teşhis edilir ve düzelmesi beklenir.

## Sonraki adım
Server SSH hazır mı? Hazırsa dest ping + kurulum komutunu veriyorum.
