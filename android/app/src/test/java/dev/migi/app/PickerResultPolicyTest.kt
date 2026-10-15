package dev.migi.app

import org.junit.Assert.assertEquals
import org.junit.Test

class PickerResultPolicyTest {
    @Test
    fun multipleSelectionKeepsEveryPhotoInOrderWithoutRepeatingSingleUri() {
        assertEquals(
            listOf("photo-1", "photo-2", "photo-3"),
            PickerResultPolicy.selectedItems("photo-1", listOf("photo-1", "photo-2", "photo-3")),
        )
    }

    @Test
    fun multipleSelectionWorksWithoutSingleUriAndSkipsMissingItems() {
        assertEquals(
            listOf("photo-1", "photo-2"),
            PickerResultPolicy.selectedItems(null, listOf("photo-1", null, "photo-2", "photo-1")),
        )
    }

    @Test
    fun singleSelectionAndEmptyResultsStillWork() {
        assertEquals(listOf("photo"), PickerResultPolicy.selectedItems("photo", emptyList()))
        assertEquals(emptyList<String>(), PickerResultPolicy.selectedItems<String>(null, listOf(null)))
    }

    @Test
    fun cancelledPickerDoesNotBecomeAnUpload() {
        assertEquals(
            PickerResultPolicy.Outcome.CANCELLED,
            PickerResultPolicy.classify(succeeded = false, hasUri = false),
        )
        assertEquals(
            PickerResultPolicy.Outcome.CANCELLED,
            PickerResultPolicy.classify(succeeded = false, hasUri = true),
        )
    }

    @Test
    fun successfulPickerRequiresAUri() {
        assertEquals(
            PickerResultPolicy.Outcome.MISSING_URI,
            PickerResultPolicy.classify(succeeded = true, hasUri = false),
        )
        assertEquals(
            PickerResultPolicy.Outcome.SELECTED,
            PickerResultPolicy.classify(succeeded = true, hasUri = true),
        )
    }
}
