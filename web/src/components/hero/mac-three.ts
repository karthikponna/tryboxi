import {
  CanvasTexture,
  Group,
  LinearMipmapLinearFilter,
  PerspectiveCamera,
  Raycaster,
  Scene,
  SRGBColorSpace,
  Vector2,
  WebGLRenderer,
} from "three";
import { buildIMac, buildMacMini, materials } from "./mac-models";
import { Screen } from "./screen";

const FRAME_MS = 1000 / 30;

export type MacSceneHandle = { spawn: () => string; dispose: () => void };

// Each await hands the main thread back so setup never becomes one long task.
const yieldTask = () => new Promise<void>((r) => setTimeout(r, 0));

function softwareGL(renderer: WebGLRenderer) {
  const gl = renderer.getContext();
  const info = gl.getExtension("WEBGL_debug_renderer_info");
  const name = info ? String(gl.getParameter(info.UNMASKED_RENDERER_WEBGL)) : "";
  return /swiftshader|llvmpipe|software|basic render/i.test(name);
}

export async function startMacScene(o: {
  host: HTMLElement;
  reduced: boolean;
  mono: string;
  sans: string;
  onSpawn: (title: string, count: number) => void;
  onReady: () => void;
}): Promise<MacSceneHandle> {
  const renderer = new WebGLRenderer({ antialias: true, alpha: true, powerPreference: "high-performance" });
  const software = softwareGL(renderer);
  const still = o.reduced || software;
  renderer.setPixelRatio(software ? 1 : Math.min(2, window.devicePixelRatio || 1));
  renderer.outputColorSpace = SRGBColorSpace;
  renderer.setClearColor(0x000000, 0);
  const canvas = renderer.domElement;
  canvas.style.display = "block";
  canvas.style.width = "100%";
  canvas.style.height = "100%";

  const screen = new Screen({ mono: o.mono, sans: o.sans, reduced: still });
  screen.spawn(0, false);
  screen.spawn(0, false);
  screen.draw();
  await yieldTask();

  const scene = new Scene();
  const tex = new CanvasTexture(screen.canvas);
  tex.colorSpace = SRGBColorSpace;
  tex.anisotropy = Math.min(8, renderer.capabilities.getMaxAnisotropy());
  tex.minFilter = LinearMipmapLinearFilter;

  const m = materials();
  const rig = new Group();
  const imac = buildIMac(tex, m);
  rig.add(imac);
  const mini = buildMacMini(m);
  mini.position.set(35, 0, 10);
  mini.rotation.y = -0.42;
  rig.add(mini);
  rig.position.set(-3, 0, 0);
  scene.add(rig);

  const camera = new PerspectiveCamera(24, 1, 1, 1000);
  camera.position.set(-25, 39, 166);
  camera.lookAt(5, 23, 0);
  await yieldTask();

  await renderer.compileAsync(scene, camera);
  await yieldTask();

  let width = 0;
  let height = 0;
  const resize = () => {
    const r = o.host.getBoundingClientRect();
    width = Math.max(1, Math.round(r.width));
    height = Math.max(1, Math.round(r.height));
    renderer.setSize(width, height, false);
    camera.aspect = width / height;
    // keep both machines in frame on narrow, tall containers
    camera.fov = camera.aspect < 1.1 ? 24 * (1.1 / camera.aspect) ** 0.85 : 24;
    camera.updateProjectionMatrix();
    render();
  };

  const ray = new Raycaster();
  const ndc = new Vector2();
  const hits = (e: PointerEvent) => {
    const r = canvas.getBoundingClientRect();
    ndc.set(((e.clientX - r.left) / r.width) * 2 - 1, -((e.clientY - r.top) / r.height) * 2 + 1);
    ray.setFromCamera(ndc, camera);
    return ray.intersectObjects([imac, mini], true).length > 0;
  };

  let yaw = 0;
  let yawTarget = 0;
  let pitch = 0;
  let pitchTarget = 0;
  const onMove = (e: PointerEvent) => {
    const r = canvas.getBoundingClientRect();
    yawTarget = ((e.clientX - r.left) / r.width - 0.5) * 0.16;
    pitchTarget = ((e.clientY - r.top) / r.height - 0.5) * 0.05;
    canvas.style.cursor = hits(e) ? "pointer" : "default";
  };
  const onLeave = () => {
    yawTarget = 0;
    pitchTarget = 0;
  };

  const spawn = () => {
    const title = screen.spawn(performance.now(), true);
    screen.draw();
    tex.needsUpdate = true;
    render();
    o.onSpawn(title, screen.count);
    return title;
  };
  const onClick = (e: PointerEvent) => {
    if (hits(e)) spawn();
  };

  function render() {
    renderer.render(scene, camera);
  }

  let raf = 0;
  let last = 0;
  let visible = true;
  const loop = (now: number) => {
    raf = requestAnimationFrame(loop);
    if (now - last < FRAME_MS - 2) return;
    last = now;
    if (screen.tick(now)) {
      screen.draw();
      tex.needsUpdate = true;
    }
    yaw += (yawTarget + Math.sin(now / 4200) * 0.05 - yaw) * 0.06;
    pitch += (pitchTarget - pitch) * 0.06;
    rig.rotation.y = yaw;
    rig.rotation.x = pitch;
    render();
  };
  const start = () => {
    if (raf || still || !visible || document.hidden) return;
    raf = requestAnimationFrame(loop);
  };
  const stop = () => {
    cancelAnimationFrame(raf);
    raf = 0;
  };

  o.host.appendChild(canvas);
  resize();
  o.onReady();
  start();

  const ro = new ResizeObserver(resize);
  ro.observe(o.host);
  const io = new IntersectionObserver(([e]) => {
    visible = e.isIntersecting;
    if (visible) start();
    else stop();
  });
  io.observe(o.host);
  const onVis = () => (document.hidden ? stop() : start());
  document.addEventListener("visibilitychange", onVis);
  canvas.addEventListener("pointermove", onMove);
  canvas.addEventListener("pointerleave", onLeave);
  canvas.addEventListener("pointerup", onClick);

  return {
    spawn,
    dispose: () => {
      stop();
      ro.disconnect();
      io.disconnect();
      document.removeEventListener("visibilitychange", onVis);
      canvas.removeEventListener("pointermove", onMove);
      canvas.removeEventListener("pointerleave", onLeave);
      canvas.removeEventListener("pointerup", onClick);
      scene.traverse((obj) => {
        const mesh = obj as unknown as { geometry?: { dispose(): void }; material?: { dispose(): void; map?: { dispose(): void } } };
        mesh.geometry?.dispose();
        mesh.material?.map?.dispose();
        mesh.material?.dispose();
      });
      tex.dispose();
      renderer.dispose();
      canvas.remove();
    },
  };
}
