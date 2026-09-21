# DESIGN.md — Aegis Visual Design System

## 1. Visual World & Aesthetic
* **Theme**: Deep Obsidian & Dynamic Spotlight Glow (inspired by RedSun).
* **Atmosphere**: Futuristic, technical, high-craft, hardware-level precision.
* **Lighting Model**: Dynamic scroll-reactive ambient light beams tracking page depth; cursor-tracking radial spotlights illuminating glassmorphic card borders and surfaces.

## 2. Color Palette & Design Tokens
* **Backgrounds**:
  * Root Background: `#070709` (Deep Obsidian)
  * Surface Background: `#0c0d14` (Tinted Dark Slate)
  * Elevated Card: `rgba(12, 13, 18, 0.8)` with `backdrop-blur-xl`
* **Borders & Dividers**:
  * Default Subtle: `rgba(255, 255, 255, 0.08)`
  * Interactive Hover: `rgba(255, 255, 255, 0.18)`
  * Active Light Beam: `linear-gradient(to right, transparent, #f43f5e, transparent)`
* **Accents & States**:
  * Primary Accent: `#f43f5e` (Aegis Crimson / Rose)
  * Secondary Accent: `#fb923c` (Molten Amber / Orange)
  * Quorum Success: `#34d399` (Emerald 400)
  * Failure / Partition: `#f43f5e` (Rose 500)
  * Consensus Term & Index: `#60a5fa` (Blue 400)

## 3. Typography Hierarchy
* **Headings**: Clean, high-contrast solid text (`#ffffff` and `#f43f5e`).
  * *Banned*: Blurry rainbow gradient text (`bg-clip-text`) on body or headings.
* **Body**: High-readability tinted grays (`#94a3b8` / `#cbd5e1`).
  * *Banned*: Washed-out gray text on colored badge backgrounds.
* **Code & Telemetry**: Monospace (`font-mono`) with tight tracking and precise optical alignment.

## 4. Motion & Micro-Interactions
* **Spring Physics**: `stiffness: 80`, `damping: 25`, `restDelta: 0.001` via Framer Motion.
* **Scroll Lighting**: Continuous `scrollYProgress` mapped to spotlight beam Y-translation and opacity.
* **Hover State**: Subtle `scale-[1.02]` with smooth border glow enhancement; no bouncy or elastic cartoon easing.

## 5. Impeccable Anti-Pattern Guardrails
1. **No Card-Soup**: Avoid nesting cards within cards. Use structural borders and negative space.
2. **No Purple-Blue Gradients**: Stick to the committed palette (Obsidian, Crimson, Amber).
3. **High Contrast Compliance**: All text must meet WCAG AAA contrast against background surfaces.
4. **Purposeful Motion**: Every animation must communicate system state (e.g. heartbeat pulsing, offset increments).
