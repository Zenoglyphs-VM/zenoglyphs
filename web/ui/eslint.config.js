import pluginVue from 'eslint-plugin-vue'
import {defineConfigWithVueTs, vueTsConfigs} from '@vue/eslint-config-typescript'

// ESLint 9 flat config, matching nom-webwallet's so the two codebases lint the
// same way. Formatting is Prettier's job, so the Vue preset is `essential`
// (correctness only) rather than `recommended` (which also enforces layout).
export default defineConfigWithVueTs(
  {
    name: 'app/files-to-lint',
    files: ['**/*.{ts,mts,tsx,vue}'],
  },
  {
    name: 'app/files-to-ignore',
    ignores: ['**/dist/**', 'node_modules/**'],
  },

  pluginVue.configs['flat/essential'],
  vueTsConfigs.recommended,

  {
    name: 'app/rules',
    rules: {
      // Every component here lives in src/components and is named for the one
      // thing it renders.
      'vue/multi-word-component-names': 'off',
      '@typescript-eslint/no-explicit-any': 'warn',
      'no-console': ['warn', {allow: ['warn', 'error']}],
      // Exactly one v-html in this app, and it carries the argument for why.
      'vue/no-v-html': 'warn',
      '@typescript-eslint/no-non-null-assertion': 'warn',
    },
  },
)
