<template>
  <!-- "Rune Z" — mitred joins, square caps, one weight, plus a bind-stroke
       struck across the diagonal, filled/stroked with the plasma gradient.
       Adopted from the round-two mark exploration in
       ../assets/logos/zenoglyphs/ (see that folder's README for the other
       seven considered and why this one won: nothing rounded anywhere,
       which is the radius token's own logic ("tight, echoing the angular
       Z") taken to the mark, and it reads as a carved character rather
       than a traced route — the right shift for the zenoglyphs rebrand.

       The gradient is declared inline rather than as a CSS variable because
       an SVG fill/stroke cannot reference one, and the id is namespaced so a
       second copy of this mark on the page cannot collide with it. -->
  <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
    <defs>
      <linearGradient id="zcribe-plasma" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%" stop-color="hsl(120 86% 63%)" />
        <stop offset="100%" stop-color="hsl(145 100% 38%)" />
      </linearGradient>
    </defs>
    <g
      stroke="url(#zcribe-plasma)"
      stroke-width="2.6"
      stroke-linecap="square"
      stroke-linejoin="miter"
    >
      <path d="M5.3,5.3H18.7L5.3,18.7H18.7" />
      <path d="M9.6,9.6L14.4,14.4" />
    </g>
  </svg>
</template>
