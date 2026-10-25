import {
  CanvasTexture,
  CircleGeometry,
  CylinderGeometry,
  DoubleSide,
  ExtrudeGeometry,
  Group,
  Mesh,
  MeshBasicMaterial,
  MeshMatcapMaterial,
  PlaneGeometry,
  Shape,
  ShapeGeometry,
  SRGBColorSpace,
  type Texture,
} from "three";
import { SVGLoader } from "three/addons/loaders/SVGLoader.js";

// Real-world dimensions in centimetres.
// iMac 24" (M4): 54.7 wide, 46.1 tall with stand, 1.15 thin, 14.7 deep stand.
// Mac mini (M4): 12.7 x 12.7 x 5.0.
export const IMAC = { w: 54.7, h: 37.6, d: 1.15, lift: 8.5, screenW: 52.2, screenH: 29.4, bezelTop: 1.2, chin: 6.2 };
export const MINI = { w: 12.7, d: 12.7, h: 5.0 };

const APPLE =
  "M12.152 6.896c-.948 0-2.415-1.078-3.96-1.04-2.04.027-3.91 1.183-4.961 3.014-2.117 3.675-.546 9.103 1.519 12.09 1.013 1.454 2.208 3.09 3.792 3.039 1.52-.065 2.09-.987 3.935-.987 1.831 0 2.35.987 3.96.948 1.637-.026 2.676-1.48 3.676-2.948 1.156-1.688 1.636-3.325 1.662-3.415-.039-.013-3.182-1.221-3.22-4.857-.026-3.04 2.48-4.494 2.597-4.559-1.429-2.09-3.623-2.324-4.39-2.376-2-.156-3.675 1.09-4.61 1.09zM15.53 3.83c.843-1.012 1.4-2.427 1.245-3.83-1.207.052-2.662.805-3.532 1.818-.78.896-1.454 2.338-1.273 3.714 1.338.104 2.715-.688 3.559-1.701";

// A flat logo `height` cm tall, centred on the origin, facing +z.
function appleLogo(height: number) {
  const data = new SVGLoader().parse(`<svg xmlns="http://www.w3.org/2000/svg"><path d="${APPLE}"/></svg>`);
  const shapes = data.paths.flatMap((p) => p.toShapes());
  const geo = new ShapeGeometry(shapes, 12);
  geo.computeBoundingBox();
  const b = geo.boundingBox!;
  const s = height / (b.max.y - b.min.y);
  geo.translate(-(b.min.x + b.max.x) / 2, -(b.min.y + b.max.y) / 2, 0);
  geo.scale(s, -s, 1);
  return geo;
}

function rect(w: number, h: number, rTop: number, rBottom: number) {
  const s = new Shape();
  const x = -w / 2;
  const y = -h / 2;
  s.moveTo(x + rBottom, y);
  s.lineTo(x + w - rBottom, y);
  s.quadraticCurveTo(x + w, y, x + w, y + rBottom);
  s.lineTo(x + w, y + h - rTop);
  s.quadraticCurveTo(x + w, y + h, x + w - rTop, y + h);
  s.lineTo(x + rTop, y + h);
  s.quadraticCurveTo(x, y + h, x, y + h - rTop);
  s.lineTo(x, y + rBottom);
  s.quadraticCurveTo(x, y, x + rBottom, y);
  return s;
}

// Matcaps: a pre-lit studio sphere per finish. No lights, no environment map, and one
// cheap shader, so the scene is ready in milliseconds instead of hundreds.
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
  return new MeshMatcapMaterial({ matcap: tex });
}

export const materials = () => ({
  orange: matcap(
    [
      [0, "#ffb690"],
      [0.32, "#f0804f"],
      [0.68, "#d65b2c"],
      [1, "#8a3214"],
    ],
    0.32,
    "rgba(80,24,6,0.5)",
  ),
  peach: matcap(
    [
      [0, "#fff4ec"],
      [0.35, "#fbd3b8"],
      [0.7, "#f0ae86"],
      [1, "#b8714b"],
    ],
    0.35,
    "rgba(110,50,20,0.4)",
  ),
  logoOrange: matcap(
    [
      [0, "#ffd2b8"],
      [0.4, "#e8693a"],
      [1, "#6e240b"],
    ],
    0.6,
    "rgba(60,16,4,0.5)",
  ),
  bezel: matcap(
    [
      [0, "#ffffff"],
      [0.55, "#f4f4f2"],
      [1, "#cfcfcc"],
    ],
    0.3,
    "rgba(120,120,118,0.4)",
  ),
  aluminum: matcap(
    [
      [0, "#ffffff"],
      [0.3, "#e8eaed"],
      [0.65, "#bcc0c5"],
      [1, "#82878d"],
    ],
    0.45,
    "rgba(60,64,70,0.55)",
  ),
  logoBlack: matcap(
    [
      [0, "#5a5a5f"],
      [0.35, "#1c1c1e"],
      [1, "#030303"],
    ],
    0.4,
    "rgba(0,0,0,0.6)",
  ),
  base: matcap(
    [
      [0, "#3a3a3d"],
      [1, "#0d0d0e"],
    ],
    0.15,
    "rgba(0,0,0,0.5)",
  ),
  port: new MeshBasicMaterial({ color: 0x0b0b0c }),
});

type Mats = ReturnType<typeof materials>;

export function buildIMac(screen: Texture, m: Mats) {
  const g = new Group();
  const { w, h, d, lift, chin } = IMAC;
  const cy = lift + h / 2;

  // the coloured aluminium shell: sides and back
  const body = new Mesh(
    new ExtrudeGeometry(rect(w - 0.2, h - 0.2, 1.3, 1.3), {
      depth: d - 0.2,
      bevelEnabled: true,
      bevelThickness: 0.1,
      bevelSize: 0.1,
      bevelSegments: 3,
      curveSegments: 10,
    }),
    m.orange,
  );
  body.position.set(0, cy, -d + 0.1);
  g.add(body);

  // pastel chin on the front, below the glass
  const chinPlate = new Mesh(new ShapeGeometry(rect(w - 0.08, chin, 0, 1.36), 10), m.peach);
  chinPlate.position.set(0, lift + chin / 2, 0.012);
  g.add(chinPlate);

  // white glass with the display set into it
  const glassH = h - chin;
  const glass = new Mesh(new ShapeGeometry(rect(w - 0.08, glassH, 1.3, 0), 10), m.bezel);
  glass.position.set(0, lift + chin + glassH / 2, 0.014);
  g.add(glass);

  const screenY = lift + h - IMAC.bezelTop - IMAC.screenH / 2;
  const border = new Mesh(new PlaneGeometry(IMAC.screenW + 0.5, IMAC.screenH + 0.5), new MeshBasicMaterial({ color: 0x050505 }));
  border.position.set(0, screenY, 0.02);
  g.add(border);

  const sc = new Mesh(new PlaneGeometry(IMAC.screenW, IMAC.screenH), new MeshBasicMaterial({ map: screen }));
  sc.position.set(0, screenY, 0.03);
  sc.name = "screen";
  g.add(sc);

  const sheen = new Mesh(
    new PlaneGeometry(IMAC.screenW, IMAC.screenH),
    new MeshBasicMaterial({ map: sheenTexture(), transparent: true, depthWrite: false }),
  );
  sheen.position.set(0, screenY, 0.04);
  g.add(sheen);

  const cam = new Mesh(new CircleGeometry(0.17, 20), new MeshBasicMaterial({ color: 0x1a1a1a }));
  cam.position.set(0, lift + h - IMAC.bezelTop / 2 + 0.05, 0.03);
  g.add(cam);

  // Apple logo on the back
  const logo = new Mesh(appleLogo(9.5), m.logoOrange);
  logo.material.side = DoubleSide;
  logo.rotation.y = Math.PI;
  logo.position.set(0, lift + h * 0.6, -d - 0.01);
  g.add(logo);

  // the stand: one bent aluminium plate behind the display, foot running forward
  const t = 0.55;
  const attach = lift + 10;
  const profile = new Shape();
  profile.moveTo(-d, attach);
  profile.lineTo(-d - t, attach);
  profile.lineTo(-7.4, t + 0.5);
  profile.quadraticCurveTo(-7.6, 0, -6.6, 0);
  profile.lineTo(7.6, 0);
  profile.quadraticCurveTo(7.9, 0, 7.9, t * 0.5);
  profile.quadraticCurveTo(7.9, t, 7.6, t);
  profile.lineTo(-6.4, t);
  profile.quadraticCurveTo(-6.95, t, -6.85, t + 0.6);
  profile.lineTo(-d, attach);
  const standW = 13.4;
  const standGeo = new ExtrudeGeometry(profile, { depth: standW, bevelEnabled: false, curveSegments: 8 });
  standGeo.rotateY(-Math.PI / 2);
  standGeo.translate(standW / 2, 0, 0);
  g.add(new Mesh(standGeo, m.peach));

  const hinge = new Mesh(new CylinderGeometry(0.85, 0.85, standW + 0.3, 28), m.aluminum);
  hinge.rotation.z = Math.PI / 2;
  hinge.position.set(0, attach - 0.6, -d - 0.45);
  g.add(hinge);

  g.add(shadow(30, 22, 0.55, 0, 0.6));
  return g;
}

export function buildMacMini(m: Mats) {
  const g = new Group();
  const { w, d, h } = MINI;
  const baseH = 0.35;
  const shell = new ExtrudeGeometry(rect(w - 0.5, d - 0.5, 2.4, 2.4), {
    depth: h - baseH - 0.5,
    bevelEnabled: true,
    bevelThickness: 0.25,
    bevelSize: 0.25,
    bevelSegments: 5,
    curveSegments: 18,
  });
  shell.rotateX(-Math.PI / 2);
  const body = new Mesh(shell, m.aluminum);
  body.position.y = baseH + 0.25;
  g.add(body);

  const baseGeo = new ExtrudeGeometry(rect(w - 1.4, d - 1.4, 2, 2), { depth: baseH + 0.1, bevelEnabled: false, curveSegments: 16 });
  baseGeo.rotateX(-Math.PI / 2);
  g.add(new Mesh(baseGeo, m.base));

  // front: two USB-C ports on the left, status light and headphone jack on the right
  const front = d / 2 + 0.006;
  const portY = baseH + 2.25;
  for (const x of [-4.2, -3.05]) {
    const port = new Mesh(new ShapeGeometry(rect(0.32, 0.88, 0.16, 0.16), 6), m.port);
    port.position.set(x, portY, front);
    g.add(port);
  }
  const led = new Mesh(new CircleGeometry(0.075, 14), new MeshBasicMaterial({ color: 0xf2f2f2 }));
  led.position.set(2.3, portY, front);
  g.add(led);
  const jack = new Mesh(new CircleGeometry(0.2, 22), m.port);
  jack.position.set(3.45, portY, front);
  g.add(jack);

  // black Apple logo on top, leaf toward the back
  const logo = new Mesh(appleLogo(4.6), m.logoBlack);
  logo.material.side = DoubleSide;
  logo.rotation.x = -Math.PI / 2;
  logo.position.set(0, h + 0.012, 0);
  g.add(logo);

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
