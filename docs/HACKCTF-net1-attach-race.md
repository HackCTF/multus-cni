# HackCTF — investigación del race de attach de red secundaria (`net1`)

> Rama: `hackctf/net1-race-fix` (base `v4.3.0`). Cluster: Combate (OVN-Kubernetes + Multus thin + CRI-O). Fecha: 2026-07-15.

## Síntoma

Al crear pods en lotes concurrentes con una anotación `k8s.v1.cni.cncf.io/networks`, una fracción (~13%) queda `Running/Ready` **sin la interfaz secundaria `net1`**, **sin ningún error**. Recrear el pod lo resuelve. Afecta a cualquier device con red secundaria (labs de Kumi). Rompe el arranque "instantáneo" de laboratorios.

## Causa raíz REAL (confirmada)

**No es un bug de Multus. Es una misconfiguración de CNI del nodo: CRI-O a veces networkea el pod con `ovn-kubernetes` DIRECTAMENTE, saltándose Multus.**

El `/etc/cni/net.d` de los workers contiene, además del wrapper de Multus, configs CNI de OVN-K standalone:

```
00-multus.conf            (type=multus)               <- default esperado
05-ovn-kubernetes.conf    (type=ovn-k8s-cni-overlay)  <- OVN directo, sin multus (stale, 10-jul)
10-ovn-kubernetes.conf    (type=ovn-k8s-cni-overlay)  <- OVN directo, sin multus (idéntico al 05)
```

CRI-O elige la primera config CNI válida por orden lexicográfico. `thin_entrypoint` **regenera `00-multus.conf` periódicamente** (con `--multus-conf-file=auto`). Durante esa ventana de regeneración, CRI-O cae a la siguiente conf válida — `05-ovn-kubernetes.conf` — y networkea el pod con **OVN directo, sin Multus** → el pod obtiene `eth0` pero **nunca se invoca Multus** → sin redes secundarias → **sin `net1`**.

### Evidencia (experimento instrumentado)

Con un build instrumentado (log hardcodeado a `/var/log/multus-hackctf.log`, bypass de la conf), en la misma tanda:

- **Pod exitoso**: `GetPod ... annotEmpty=false annot="rb-race-net"` + `CmdAdd ... delegatesLoaded=1`. Multus corrió y cargó `net1`.
- **Pod fallido**: **CERO líneas de log de Multus** → Multus no fue invocado.

Journal de CRI-O para el pod fallido:
```
Adding pod user-superadmin_<pod> to CNI network "ovn-kubernetes" (type=ovn-k8s-cni-overlay)
```
→ CRI-O lo networkeó con OVN directo, sin pasar por `multus-cni-network`.

Esto explica todo: intermitente (coincidir con la ventana de regeneración), silencioso (Multus no corre, no hay error), peor con concurrencia, y "recrear lo arregla" (`00-multus.conf` ya estable).

## Hipótesis previas descartadas

1. **"Multus (thick) lee del informer cache stale"** — FALSO: el despliegue es **thin** (lectura viva a la API), no thick.
2. **"La GET viva devuelve la anotación vacía"** — el patch de esta rama (re-read en anotación vacía) **NO redujo el fallo** (8/60), porque Multus **ni se invoca** para los pods fallidos. El instrumentado lo probó.

## Qué contiene esta rama (hardening, NO el fix de este bug)

`pkg/multus/multus.go` — en `GetPod`, en CNI ADD, si el pod se leyó con la anotación de redes **vacía**, re-lee con `GetPodAPILiveQuery` (hasta 3× / 100ms) antes de concluir "no networks". Cierra un caso patológico genuino (lag informer→anotación vacía) que existe en la ruta de código (`k8sclient.go` `NoK8sNetworkError` → 0 delegados sin error), pero que **no** es el mecanismo del race observado en Combate. Se conserva como defensa en profundidad, con tests (`pkg/multus/multus_race_test.go`).

## El fix REAL

A nivel de config CNI del nodo — que CRI-O **solo** vea `00-multus.conf`:

1. **`--rename-conf-file`** (flag de `thin_entrypoint`, diseñado para esto): renombra la conf "master" (OVN-K) para que CRI-O no la use como default directamente.
2. Eliminar la conf duplicada/stale (`05-ovn-kubernetes.conf`).
3. (Opcional) regeneración atómica de `00-multus.conf` para no dejar ventana.

Y como **defensa en profundidad a nivel plataforma**: **Kumi verifica `net1` y auto-repara** (recrea el device si falta) — validado en el PoC VXLAN (recuperó un pod que perdió `net1`).
