import { useI18n } from './useI18n'

export function useProcessState() {
  const { t } = useI18n()

  const translateDesiredState = (state: string): string => {
    return t(`process:desiredState.${state}`)
  }

  const translateObservedState = (state: string): string => {
    return t(`process:observedState.${state}`)
  }

  const translateHealthState = (state: string): string => {
    return t(`process:healthState.${state}`)
  }

  return {
    translateDesiredState,
    translateObservedState,
    translateHealthState,
  }
}
