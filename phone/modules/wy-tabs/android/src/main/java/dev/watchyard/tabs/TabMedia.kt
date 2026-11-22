package dev.watchyard.tabs

import android.Manifest
import android.app.Activity
import android.content.ClipData
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.provider.MediaStore
import android.webkit.PermissionRequest
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import androidx.core.content.ContextCompat
import androidx.core.content.FileProvider
import expo.modules.interfaces.permissions.PermissionsStatus
import expo.modules.kotlin.AppContext
import java.io.File

/**
 * phone.md P17.5: the mic and the file chooser (with a camera photo) of a claude.ai tab. The
 * pure rules (which origin, which resources, when to offer the camera) are in [TabPolicy]. One
 * file chooser at a time; its callback is answered exactly once (a new chooser, a destroyed tab
 * and Cancel all answer null).
 */
object TabMedia {
  private const val REQUEST_CODE = 0x5754
  private val main = Handler(Looper.getMainLooper())

  private var callback: ValueCallback<Array<Uri>>? = null
  private var params: WebChromeClient.FileChooserParams? = null
  private var photo: Uri? = null
  private var photoFile: File? = null

  /** Grants a page's mic/camera request after the Android permissions; denies on anything else. */
  fun permission(appContext: AppContext, request: PermissionRequest) {
    val resources = TabPolicy.grantable(request.origin?.toString(), request.resources.toList())
    if (resources.isEmpty()) return request.deny()
    val needed = buildList {
      if (TabPolicy.AUDIO_CAPTURE in resources) add(Manifest.permission.RECORD_AUDIO)
      if (TabPolicy.VIDEO_CAPTURE in resources) add(Manifest.permission.CAMERA)
    }
    val perms = appContext.permissions
    if (perms == null) return request.deny()
    perms.askForPermissions({ result ->
      main.post {
        val ok = needed.all { result[it]?.status == PermissionsStatus.GRANTED }
        try {
          if (ok) request.grant(resources.toTypedArray()) else request.deny()
        } catch (_: IllegalStateException) { // the page cancelled the request meanwhile
        }
      }
    }, *needed.toTypedArray())
  }

  /** Opens the system file chooser, plus a camera option for image types. Returns false if none can open. */
  fun chooseFile(
    appContext: AppContext,
    cb: ValueCallback<Array<Uri>>,
    p: WebChromeClient.FileChooserParams,
  ): Boolean {
    val activity = appContext.currentActivity
    if (activity == null) {
      cb.onReceiveValue(null)
      return true
    }
    cancel() // a chooser still pending is answered (null) before a new one opens
    callback = cb
    params = p
    val wantsPhoto = TabPolicy.acceptsImages(p.acceptTypes.toList())
    if (!wantsPhoto) return launch(activity, false)
    if (ContextCompat.checkSelfPermission(activity, Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) {
      return launch(activity, true)
    }
    // Declaring CAMERA makes ACTION_IMAGE_CAPTURE need it granted; without it, offer files only.
    val perms = appContext.permissions
    if (perms == null) return launch(activity, false)
    perms.askForPermissions({ result ->
      main.post {
        if (callback === cb) launch(activity, result[Manifest.permission.CAMERA]?.status == PermissionsStatus.GRANTED)
      }
    }, Manifest.permission.CAMERA)
    return true
  }

  private fun launch(activity: Activity, withCamera: Boolean): Boolean {
    if (callback == null) return true
    val p = params ?: return finish(null)
    try {
      val content = p.createIntent()
      var intent = content
      if (withCamera) {
        cameraIntent(activity)?.let {
          intent = Intent.createChooser(content, p.title?.takeIf { t -> t.isNotEmpty() }).putExtra(Intent.EXTRA_INITIAL_INTENTS, arrayOf(it))
        }
      }
      activity.startActivityForResult(intent, REQUEST_CODE)
    } catch (e: Exception) {
      finish(null)
    }
    return true
  }

  private fun cameraIntent(activity: Activity): Intent? {
    return try {
      val dir = File(activity.cacheDir, "wy-tabs-camera").apply { mkdirs() }
      dir.listFiles()?.forEach { it.delete() } // earlier photos are not kept
      val file = File.createTempFile("photo-", ".jpg", dir)
      val uri = FileProvider.getUriForFile(activity, "${activity.packageName}.wytabs.files", file)
      photoFile = file
      photo = uri
      Intent(MediaStore.ACTION_IMAGE_CAPTURE).apply {
        putExtra(MediaStore.EXTRA_OUTPUT, uri)
        clipData = ClipData.newRawUri("", uri)
        addFlags(Intent.FLAG_GRANT_WRITE_URI_PERMISSION or Intent.FLAG_GRANT_READ_URI_PERMISSION)
      }
    } catch (e: Exception) {
      photo = null
      photoFile = null
      null
    }
  }

  /** From the module's OnActivityResult. */
  fun onResult(requestCode: Int, resultCode: Int, data: Intent?) {
    if (requestCode != REQUEST_CODE) return
    var result: Array<Uri>? = null
    if (resultCode == Activity.RESULT_OK) {
      val uri = photo
      val file = photoFile
      result = if (data?.data != null || data?.clipData != null) {
        WebChromeClient.FileChooserParams.parseResult(resultCode, data)
      } else if (uri != null && file != null && file.length() > 0) {
        arrayOf(uri)
      } else null
    }
    finish(result)
  }

  /** Answers a chooser still pending with "nothing chosen". */
  fun cancel() {
    if (callback != null) finish(null)
  }

  private fun finish(result: Array<Uri>?): Boolean {
    val cb = callback
    callback = null
    params = null
    if (result == null) photoFile?.delete()
    photo = null
    photoFile = null
    cb?.onReceiveValue(result)
    return true
  }
}
