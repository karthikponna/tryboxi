import {
  CanvasTexture,
  CircleGeometry,
  ExtrudeGeometry,
  Group,
  Mesh,
  MeshBasicMaterial,
  MeshMatcapMaterial,
  PlaneGeometry,
  Shape,
  SRGBColorSpace,
  type Texture,
} from "three";

// Real-world dimensions in centimetres.
// iMac 24" (M4): 54.7 wide, 46.1 tall with stand, 1.15 thin, 14.7 deep stand.
// Mac mini (M4): 12.7 x 12.7 x 5.0.
export const IMAC = { w: 54.7, h: 37.6, d: 1.15, lift: 8.5, screenW: 52.2, screenH: 29.4, bezelTop: 1.2 };
export const MINI = { w: 12.7, d: 12.7, h: 5.0 };

function roundedRect(w: number, h: number, r: number, cx = 0, cy = 0) {
  const s = new Shape();
  const x = cx - w / 2;
  const y = cy - h / 2;
  s.moveTo(x + r, y);
  s.lineTo(x + w - r, y);
  s.quadraticCurveTo(x + w, y, x + w, y + r);
  s.lineTo(x + w, y + h - r);
  s.quadraticCurveTo(x + w, y + h, x + w - r, y + h);
  s.lineTo(x + r, y + h);
  s.quadraticCurveTo(x, y + h, x, y + h - r);
  s.lineTo(x, y + r);
  s.quadraticCurveTo(x, y, x + r, y);
  return s;
}

// A rect with only the top corners rounded (the iMac's glass ends at the chin).
function topRoundedRect(w: number, h: number, r: number, cx: number, cy: number) {
  const s = new Shape();
  const x = cx - w / 2;
  const y = cy - h / 2;
  s.moveTo(x, y);
  s.lineTo(x + w, y);
  s.lineTo(x + w, y + h - r);
  s.quadraticCurveTo(x + w, y + h, x + w - r, y + h);
  s.lineTo(x + r, y + h);
  s.quadraticCurveTo(x, y + h, x, y + h - r);
  s.lineTo(x, y);
  return s;
}

// Matcaps: a pre-lit studio sphere per finish. No lights, no environment map, and one
// cheap shader, so the scene is ready in a few milliseconds instead of hundreds.
function matcap(stops: [number, string][], highlight: number, rim: string) {
  const s = 256;
  const c = document.createElement("canvas");
  c.width = c.height = s;
  const g = c.getContext("2d")!;
  g.beginPath();
  g.arc(s / 2, s / 2, s / 2, 0, Math.PI * 2);
  g.clip();
  const body = g.createRadialGradient(s * 0.36, s * 0.3, s * 0.02, s * 0.5, s * 0.5, s * 0.62);
  for (const [o, col] of stops) body.addColorStop(o, col);
  g.fillStyle = body;
  g.fillRect(0, 0, s, s);
  const strip = g.createLinearGradient(0, s * 0.08, 0, s * 0.34);
  strip.addColorStop(0, "rgba(255,255,255,0)");
  strip.addColorStop(0.5, `rgba(255,255,255,${highlight})`);
  strip.addColorStop(1, "rgba(255,255,255,0)");
  g.fillStyle = strip;
  g.fillRect(0, 0, s, s);
  const edge = g.createRadialGradient(s / 2, s / 2, s * 0.4, s / 2, s / 2, s / 2);
  edge.addColorStop(0, "rgba(0,0,0,0)");
  edge.addColorStop(1, rim);
  g.fillStyle = edge;
  g.fillRect(0, 0, s, s);
  const tex = new CanvasTexture(c);
  tex.colorSpace = SRGBColorSpace;
  return tex;
}

export const materials = () => ({
  aluminum: new MeshMatcapMaterial({
    matcap: matcap(
      [
        [0, "#ffffff"],
        [0.3, "#e6e8eb"],
        [0.65, "#b9bdc2"],
        [1, "#80858b"],
      ],
      0.45,
      "rgba(60,64,70,0.55)",
    ),
  }),
  bezel: new MeshMatcapMaterial({
    matcap: matcap(
      [
        [0, "#ffffff"],
        [0.55, "#f4f4f2"],
        [1, "#cfcfcc"],
      ],
      0.3,
      "rgba(120,120,118,0.4)",
    ),
  }),
  port: new MeshBasicMaterial({ color: 0x0b0b0c }),
  base: new MeshMatcapMaterial({
    matcap: matcap(
      [
        [0, "#3a3a3d"],
        [1, "#0d0d0e"],
      ],
      0.15,
      "rgba(0,0,0,0.5)",
    ),
  }),
});

export function buildIMac(screen: Texture, m: ReturnType<typeof materials>) {
  const g = new Group();
  const { w, h, d, lift } = IMAC;
  const cy = lift + h / 2;

  const body = new Mesh(
    new ExtrudeGeometry(roundedRect(w - 0.2, h - 0.2, 1.3), {
      depth: d - 0.2,
      bevelEnabled: true,
      bevelThickness: 0.1,
      bevelSize: 0.1,
      bevelSegments: 3,
      curveSegments: 10,
    }),
    m.aluminum,
  );
  body.position.set(0, cy, -d + 0.1);
  g.add(body);

  const chin = 6.2;
  const glassH = h - chin;
  const glass = new Mesh(
    new ExtrudeGeometry(topRoundedRect(w - 0.12, glassH - 0.06, 1.22, 0, 0), { depth: 0.02, bevelEnabled: false, curveSegments: 10 }),
    m.bezel,
  );
  glass.position.set(0, lift + chin + glassH / 2, 0.02);
  g.add(glass);

  const screenMat = new MeshBasicMaterial({ map: screen, toneMapped: false });
  const sc = new Mesh(new PlaneGeometry(IMAC.screenW, IMAC.screenH), screenMat);
  sc.position.set(0, lift + h - IMAC.bezelTop - IMAC.screenH / 2, 0.05);
  sc.name = "screen";
  g.add(sc);

  const sheen = new Mesh(
    new PlaneGeometry(IMAC.screenW, IMAC.screenH),
    new MeshBasicMaterial({ map: sheenTexture(), transparent: true, depthWrite: false, toneMapped: false }),
  );
  sheen.position.copy(sc.position);
  sheen.position.z += 0.01;
  g.add(sheen);

  // The stand: one bent aluminum plate, attached behind the display, foot running forward.
  const t = 0.55;
  const profile = new Shape();
  profile.moveTo(-d, lift + 10);
  profile.lineTo(-d - t, lift + 10);
  profile.lineTo(-7.4, t + 0.5);
  profile.quadraticCurveTo(-7.6, 0, -6.6, 0);
  profile.lineTo(7.6, 0);
  profile.quadraticCurveTo(7.9, 0, 7.9, t * 0.5);
  profile.quadraticCurveTo(7.9, t, 7.6, t);
  profile.lineTo(-6.4, t);
  profile.quadraticCurveTo(-6.95, t, -6.85, t + 0.6);
  profile.lineTo(-d, lift + 10);
  const standW = 13.4;
  const standGeo = new ExtrudeGeometry(profile, { depth: standW, bevelEnabled: false, curveSegments: 8 });
  standGeo.rotateY(-Math.PI / 2);
  standGeo.translate(standW / 2, 0, 0);
  g.add(new Mesh(standGeo, m.aluminum));

  g.add(shadow(30, 22, 0.55, 0, 0.6));
  return g;
}

export function buildMacMini(m: ReturnType<typeof materials>) {
  const g = new Group();
  const { w, d, h } = MINI;
  const baseH = 0.35;
  const shell = new ExtrudeGeometry(roundedRect(w - 0.5, d - 0.5, 2.3), {
    depth: h - baseH - 0.5,
    bevelEnabled: true,
    bevelThickness: 0.25,
    bevelSize: 0.25,
    bevelSegments: 4,
    curveSegments: 16,
  });
  shell.rotateX(-Math.PI / 2);
  const body = new Mesh(shell, m.aluminum);
  body.position.y = baseH + 0.25;
  g.add(body);

  const baseGeo = new ExtrudeGeometry(roundedRect(w - 1.6, d - 1.6, 1.8), { depth: baseH + 0.1, bevelEnabled: false, curveSegments: 16 });
  baseGeo.rotateX(-Math.PI / 2);
  g.add(new Mesh(baseGeo, m.base));

  const front = d / 2 + 0.005;
  const portY = baseH + 1.7;
  for (const x of [-3.9, -2.8]) {
    const port = new Mesh(new ExtrudeGeometry(roundedRect(0.86, 0.3, 0.15), { depth: 0.02, bevelEnabled: false }), m.port);
    port.position.set(x, portY, front);
    g.add(port);
  }
  const jack = new Mesh(new CircleGeometry(0.17, 20), m.port);
  jack.position.set(-1.9, portY, front + 0.02);
  g.add(jack);
  const led = new Mesh(new CircleGeometry(0.07, 12), new MeshBasicMaterial({ color: 0xe8fff2, toneMapped: false }));
  led.position.set(4.6, portY, front + 0.02);
  g.add(led);

  g.add(shadow(18, 18, 0.5, 0, 0));
  return g;
}

function shadow(w: number, d: number, opacity: number, x: number, z: number) {
  const c = document.createElement("canvas");
  c.width = c.height = 128;
  const ctx = c.getContext("2d")!;
  const grd = ctx.createRadialGradient(64, 64, 0, 64, 64, 64);
  grd.addColorStop(0, `rgba(0,0,0,${opacity})`);
  grd.addColorStop(1, "rgba(0,0,0,0)");
  ctx.fillStyle = grd;
  ctx.fillRect(0, 0, 128, 128);
  const tex = new CanvasTexture(c);
  const mesh = new Mesh(new PlaneGeometry(w, d), new MeshBasicMaterial({ map: tex, transparent: true, depthWrite: false }));
  mesh.rotation.x = -Math.PI / 2;
  mesh.position.set(x, 0.02, z);
  mesh.renderOrder = -1;
  return mesh;
}

function sheenTexture() {
  const c = document.createElement("canvas");
  c.width = 256;
  c.height = 144;
  const ctx = c.getContext("2d")!;
  const grd = ctx.createLinearGradient(0, 0, 256, 144);
  grd.addColorStop(0, "rgba(255,255,255,0.10)");
  grd.addColorStop(0.35, "rgba(255,255,255,0.02)");
  grd.addColorStop(1, "rgba(255,255,255,0)");
  ctx.fillStyle = grd;
  ctx.fillRect(0, 0, 256, 144);
  const tex = new CanvasTexture(c);
  tex.colorSpace = SRGBColorSpace;
  return tex;
}
