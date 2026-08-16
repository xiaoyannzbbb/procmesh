import js from '@eslint/js'
import tsPlugin from '@typescript-eslint/eslint-plugin'
import tsParser from '@typescript-eslint/parser'
import vuePlugin from 'eslint-plugin-vue'
import vueParser from 'vue-eslint-parser'
import i18nextPlugin from 'eslint-plugin-i18next'

export default [
  js.configs.recommended,
  {
    ignores: ['dist', 'node_modules', '.vite']
  },
  {
    files: ['src/**/*.{js,ts,vue}'],
    languageOptions: {
      parser: vueParser,
      parserOptions: {
        parser: tsParser,
        ecmaVersion: 2021,
        sourceType: 'module',
      },
      globals: {
        console: 'readonly',
        document: 'readonly',
        window: 'readonly',
        localStorage: 'readonly',
        sessionStorage: 'readonly',
        fetch: 'readonly',
        HTMLElement: 'readonly',
        getComputedStyle: 'readonly',
        HeadersInit: 'readonly',
        performance: 'readonly',
        process: 'readonly',
        crypto: 'readonly',
        AbortController: 'readonly',
        TextDecoder: 'readonly',
        BlobPart: 'readonly',
        Blob: 'readonly',
        URL: 'readonly',
      }
    },
    plugins: {
      '@typescript-eslint': tsPlugin,
      vue: vuePlugin,
      i18next: i18nextPlugin,
    },
    rules: {
      ...tsPlugin.configs.recommended.rules,
      'i18next/no-literal-string': [
        'warn',
        {
          mode: 'all',
          'should-validate-template': true,
          ignore: [
            '^[A-Z_]+$', // Constants
            '^[0-9]+$',  // Numbers
            '^\\s*$',    // Whitespace
          ],
          ignoreAttribute: [
            'data-testid',
            'type',
            'role',
            'aria-label',
            'placeholder',
            'autocomplete',
            'name',
            'class',
            'style',
            'href',
            'src',
            'alt',
            'width',
            'height',
          ],
          ignoreCallee: [
            'console.*',
            't',
            'tError',
          ],
          ignoreProperty: [
            'path',
            'component',
            'meta',
          ],
        },
      ],
    }
  }
]
